// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type linuxNetwork struct{}

func NewNetwork() Network { return linuxNetwork{} }

func (linuxNetwork) Reconcile(_ context.Context, desired DesiredState) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	route, err := gatewayUnderlayRoute(desired.Members)
	if err != nil {
		return err
	}
	underlay, err := netlink.LinkByIndex(route.LinkIndex)
	if err != nil {
		return fmt.Errorf("resolve gateway underlay interface: %w", err)
	}
	name := OverlayInterfaceName(desired.GatewayName)
	link, err := ensureGatewayVXLAN(name, desired, underlay, route)
	if err != nil {
		return err
	}
	prefix := netip.MustParsePrefix(desired.OverlayCIDR)
	address := netip.MustParseAddr(desired.GatewayAddress)
	if err := netlink.AddrReplace(link, &netlink.Addr{IPNet: &net.IPNet{IP: net.IP(address.AsSlice()), Mask: net.CIDRMask(prefix.Bits(), address.BitLen())}}); err != nil {
		return fmt.Errorf("configure gateway overlay address: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring gateway overlay up: %w", err)
	}
	return reconcileGatewayPeers(link, desired.Members)
}

func gatewayUnderlayRoute(members []Member) (netlink.Route, error) {
	if len(members) > 0 {
		firstUnderlay := netip.MustParseAddr(members[0].UnderlayIP)
		routes, err := netlink.RouteGet(net.IP(firstUnderlay.AsSlice()))
		if err != nil {
			return netlink.Route{}, fmt.Errorf("resolve gateway member underlay: %w", err)
		}
		if len(routes) == 0 || routes[0].LinkIndex <= 0 {
			return netlink.Route{}, errors.New("resolve gateway member underlay: no usable route")
		}
		return routes[0], nil
	}
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return netlink.Route{}, fmt.Errorf("resolve gateway default underlay: %w", err)
	}
	return selectDefaultUnderlayRoute(routes)
}

func selectDefaultUnderlayRoute(routes []netlink.Route) (netlink.Route, error) {
	candidates := make([]netlink.Route, 0, len(routes))
	for _, route := range routes {
		if isDefaultIPv4Route(route) && route.LinkIndex > 0 && (route.Table == 0 || route.Table == unix.RT_TABLE_MAIN) {
			candidates = append(candidates, route)
		}
	}
	if len(candidates) == 0 {
		return netlink.Route{}, errors.New("resolve gateway default underlay: no usable IPv4 default route")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].LinkIndex < candidates[j].LinkIndex
	})
	return candidates[0], nil
}

func isDefaultIPv4Route(route netlink.Route) bool {
	if route.Dst == nil {
		return true
	}
	ones, bits := route.Dst.Mask.Size()
	return ones == 0 && bits == 32 && route.Dst.IP.To4() != nil
}

func ensureGatewayVXLAN(name string, desired DesiredState, underlay netlink.Link, route netlink.Route) (netlink.Link, error) {
	if existing, err := netlink.LinkByName(name); err == nil {
		vxlan, ok := existing.(*netlink.Vxlan)
		if !ok || vxlan.VxlanId != int(desired.VNI) || vxlan.Port != desired.VXLANPort || vxlan.VtepDevIndex != underlay.Attrs().Index {
			return nil, fmt.Errorf("owned gateway overlay link %q has conflicting attributes", name)
		}
		return existing, nil
	}
	source := route.Src
	if len(source) == 0 {
		addresses, err := netlink.AddrList(underlay, netlink.FAMILY_V4)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("discover gateway underlay source address: %w", err)
		}
		source = addresses[0].IP
	}
	vxlan := &netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: name, MTU: int(desired.MTU)}, VxlanId: int(desired.VNI), VtepDevIndex: underlay.Attrs().Index, SrcAddr: source, Port: desired.VXLANPort, Learning: true, NoAge: true}
	if err := netlink.LinkAdd(vxlan); err != nil {
		return nil, fmt.Errorf("create gateway VXLAN overlay: %w", err)
	}
	return vxlan, nil
}

func reconcileGatewayPeers(link netlink.Link, members []Member) error {
	existing, err := netlink.NeighList(link.Attrs().Index, unix.AF_BRIDGE)
	if err != nil {
		return fmt.Errorf("list gateway VXLAN peers: %w", err)
	}
	zeroMAC := net.HardwareAddr{0, 0, 0, 0, 0, 0}
	for i := range existing {
		if existing[i].State&netlink.NUD_PERMANENT != 0 && existing[i].HardwareAddr.String() == zeroMAC.String() {
			if err := netlink.NeighDel(&existing[i]); err != nil && !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("remove stale gateway VXLAN peer: %w", err)
			}
		}
	}
	for _, member := range members {
		peer := netip.MustParseAddr(member.UnderlayIP)
		neighbor := &netlink.Neigh{LinkIndex: link.Attrs().Index, Family: unix.AF_BRIDGE, State: netlink.NUD_PERMANENT, Flags: netlink.NTF_SELF, IP: net.IP(peer.AsSlice()), HardwareAddr: zeroMAC}
		if err := netlink.NeighAppend(neighbor); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("install gateway VXLAN peer %s: %w", member.ID, err)
		}
	}
	return nil
}
