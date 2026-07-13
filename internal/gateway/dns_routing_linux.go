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

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	clusterDNSRulePriority  = 97
	clusterDNSRuleProtocol  = uint8(98)
	clusterDNSRouteProtocol = 98
)

type linuxDNSRouting struct{}

func NewDNSRouting() DNSRouting { return linuxDNSRouting{} }

func (linuxDNSRouting) Reconcile(_ context.Context, address netip.Addr) error {
	address = address.Unmap()
	if !address.IsValid() {
		return errors.New("cluster DNS address is invalid")
	}
	family := netlink.FAMILY_V4
	if address.Is6() {
		family = netlink.FAMILY_V6
	}
	destination := &net.IPNet{IP: net.IP(address.AsSlice()), Mask: net.CIDRMask(address.BitLen(), address.BitLen())}
	if err := reconcileClusterDNSRoute(family, destination); err != nil {
		return err
	}
	rules, err := netlink.RuleList(family)
	if err != nil {
		return fmt.Errorf("list cluster DNS policy rules: %w", err)
	}
	found := false
	for i := range rules {
		if rules[i].Protocol == clusterDNSRuleProtocol {
			if !found && rules[i].Priority == clusterDNSRulePriority && rules[i].Table == unix.RT_TABLE_MAIN && rules[i].Dst != nil && rules[i].Dst.String() == destination.String() {
				found = true
				continue
			}
			if err := netlink.RuleDel(&rules[i]); err != nil && !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("remove stale cluster DNS policy rule: %w", err)
			}
		}
	}
	if found {
		return nil
	}
	rule := netlink.NewRule()
	rule.Family = family
	rule.Priority = clusterDNSRulePriority
	rule.Table = unix.RT_TABLE_MAIN
	rule.Protocol = clusterDNSRuleProtocol
	rule.Dst = destination
	if err := netlink.RuleAdd(rule); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("add cluster DNS policy rule: %w", err)
	}
	return nil
}

func reconcileClusterDNSRoute(family int, destination *net.IPNet) error {
	routes, err := netlink.RouteList(nil, family)
	if err != nil {
		return fmt.Errorf("list routes for cluster DNS: %w", err)
	}
	var ordinaryDefault *netlink.Route
	found := false
	for i := range routes {
		route := &routes[i]
		if route.Protocol == clusterDNSRouteProtocol {
			if !found && route.Table == unix.RT_TABLE_MAIN && route.Dst != nil && route.Dst.String() == destination.String() {
				found = true
				continue
			}
			if err := netlink.RouteDel(route); err != nil && !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("remove stale cluster DNS route: %w", err)
			}
			continue
		}
		if (route.Table == 0 || route.Table == unix.RT_TABLE_MAIN) && isDefaultRoute(route.Dst) && route.LinkIndex != 0 && route.Gw != nil {
			candidate := *route
			ordinaryDefault = &candidate
		}
	}
	if found {
		return nil
	}
	if ordinaryDefault == nil {
		return errors.New("ordinary default route for cluster DNS was not found")
	}
	route := netlink.Route{
		LinkIndex: ordinaryDefault.LinkIndex,
		Dst:       destination,
		Gw:        ordinaryDefault.Gw,
		Table:     unix.RT_TABLE_MAIN,
		Protocol:  clusterDNSRouteProtocol,
	}
	if err := netlink.RouteReplace(&route); err != nil {
		return fmt.Errorf("install cluster DNS route: %w", err)
	}
	return nil
}

func isDefaultRoute(destination *net.IPNet) bool {
	if destination == nil {
		return true
	}
	ones, _ := destination.Mask.Size()
	return ones == 0
}
