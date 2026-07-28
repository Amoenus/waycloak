// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package gatewaydataplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
)

const coreTableName = "waycloak_gateway_core"

const ipv4ForwardingPath = "/proc/sys/net/ipv4/ip_forward"

type LinuxBackend struct{}

func (LinuxBackend) EnsureOverlay(_ context.Context, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	underlay, err := netlink.LinkByName(config.UnderlayInterface)
	if err != nil {
		return fmt.Errorf("resolve underlay: %w", err)
	}
	source, err := firstIPv4(underlay)
	if err != nil {
		return err
	}
	link, err := netlink.LinkByName(config.OverlayInterface)
	if err == nil {
		vxlan, ok := link.(*netlink.Vxlan)
		if !ok || link.Attrs().Alias != "waycloak:"+config.GatewayUID {
			return errors.New("overlay interface name is owned by foreign state")
		}
		if vxlan.VxlanId != int(config.VNI) || vxlan.Port != int(config.VXLANPort) || vxlan.VtepDevIndex != underlay.Attrs().Index || !vxlan.SrcAddr.Equal(source) || link.Attrs().MTU != config.MTU {
			if err := netlink.LinkDel(link); err != nil {
				return err
			}
			link = nil
		}
	} else if !isLinkNotFound(err) {
		return err
	}
	createdLink := false
	if link == nil {
		attributes := netlink.NewLinkAttrs()
		attributes.Name = config.OverlayInterface
		attributes.MTU = config.MTU
		attributes.Alias = "waycloak:" + config.GatewayUID
		vxlan := &netlink.Vxlan{LinkAttrs: attributes, VxlanId: int(config.VNI), VtepDevIndex: underlay.Attrs().Index, SrcAddr: source, Port: int(config.VXLANPort), Learning: true}
		if err := netlink.LinkAdd(vxlan); err != nil {
			return fmt.Errorf("create gateway VXLAN: %w", err)
		}
		link = vxlan
		createdLink = true
		defer func() {
			if createdLink {
				_ = netlink.LinkDel(link)
			}
		}()
		if err := netlink.LinkSetAlias(link, "waycloak:"+config.GatewayUID); err != nil {
			return fmt.Errorf("mark gateway VXLAN ownership: %w", err)
		}
	}
	prefix := netipPrefix(config.GatewayAddress, config.OverlayCIDR.Bits())
	address := &netlink.Addr{IPNet: prefix}
	if err := netlink.AddrReplace(link, address); err != nil {
		return fmt.Errorf("assign gateway overlay address: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("activate gateway overlay: %w", err)
	}
	if err := requireIPv4Forwarding(ipv4ForwardingPath); err != nil {
		return err
	}
	createdLink = false
	return nil
}

func requireIPv4Forwarding(path string) error {
	value, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("observe namespaced IPv4 forwarding: %w", err)
	}
	if strings.TrimSpace(string(value)) != "1" {
		return errors.New("namespaced IPv4 forwarding is disabled")
	}
	return nil
}

func (LinuxBackend) ReplaceRules(_ context.Context, config Config, healthy bool) error {
	if err := config.Validate(); err != nil {
		return err
	}
	connection := &nftables.Conn{}
	tables, err := connection.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if table.Name == coreTableName {
			connection.DelTable(table)
		}
	}
	table := connection.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: coreTableName})
	policy := nftables.ChainPolicyDrop
	forward := connection.AddChain(&nftables.Chain{Table: table, Name: "forward", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &policy})
	postrouting := connection.AddChain(&nftables.Chain{Table: table, Name: "postrouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
	if healthy {
		connection.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: []byte("waycloak:overlay-to-tunnel"), Exprs: interfacePair(config.OverlayInterface, config.TunnelInterface, true)})
		connection.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: []byte("waycloak:tunnel-to-overlay-established"), Exprs: append(interfacePair(config.TunnelInterface, config.OverlayInterface, false), established()...)})
		connection.AddRule(&nftables.Rule{Table: table, Chain: postrouting, UserData: []byte("waycloak:overlay-masquerade"), Exprs: []expr.Any{&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceBytes(config.OverlayInterface)}, &expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceBytes(config.TunnelInterface)}, &expr.Masq{}}})
	}
	if err := connection.Flush(); err != nil {
		return fmt.Errorf("replace gateway fail-closed rules: %w", err)
	}
	return nil
}

func interfacePair(input, output string, accept bool) []expr.Any {
	rules := []expr.Any{&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceBytes(input)}, &expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceBytes(output)}}
	if accept {
		rules = append(rules, &expr.Verdict{Kind: expr.VerdictAccept})
	}
	return rules
}
func established() []expr.Any {
	return []expr.Any{&expr.Ct{Register: 1, Key: expr.CtKeySTATE}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0x06, 0, 0, 0}, Xor: []byte{0, 0, 0, 0}}, &expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}}, &expr.Verdict{Kind: expr.VerdictAccept}}
}
func interfaceBytes(value string) []byte {
	result := make([]byte, 16)
	copy(result, value)
	return result
}
func firstIPv4(link netlink.Link) (net.IP, error) {
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if value := address.IP.To4(); value != nil {
			return value, nil
		}
	}
	return nil, errors.New("underlay has no IPv4 address")
}
func isLinkNotFound(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}
func netipPrefix(address netip.Addr, bits int) *net.IPNet {
	return &net.IPNet{IP: net.IP(address.AsSlice()), Mask: net.CIDRMask(bits, 32)}
}
