// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package gateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"

	"github.com/Amoenus/waycloak/internal/provider"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type linuxForwarding struct{}

func NewForwarding() Forwarding { return linuxForwarding{} }

func (linuxForwarding) InstallLockdown(_ context.Context, desired DesiredState) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	if err := ensureForwardingLockdown(desired.GatewayName); err != nil {
		return err
	}
	return ensureVXLANIngressLockdown(desired.GatewayName, desired.VXLANPort)
}

func (linuxForwarding) Reconcile(_ context.Context, desired DesiredState) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	if err := ensureIPv4Forwarding(); err != nil {
		return err
	}
	overlay, err := readyLink(OverlayInterfaceName(desired.GatewayName))
	if err != nil {
		return fmt.Errorf("resolve gateway overlay for forwarding: %w", err)
	}
	tunnel, err := readyLink(desired.TunnelInterface)
	if err != nil {
		return fmt.Errorf("resolve VPN tunnel for forwarding: %w", err)
	}
	if overlay.Attrs().Index == tunnel.Attrs().Index {
		return errors.New("gateway overlay and VPN tunnel interfaces must differ")
	}
	if err := replaceForwardingPolicy(desired, overlay.Attrs().Name, tunnel.Attrs().Name); err != nil {
		return err
	}
	return replaceVXLANIngressPolicy(desired)
}

func (linuxForwarding) ObservePortForwardRules(_ context.Context, desired DesiredState) ([]PortForwardRuleObservation, error) {
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	observations := make([]PortForwardRuleObservation, 0, len(desired.PortForwardLeases))
	conn := &nftables.Conn{}
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return nil, fmt.Errorf("list gateway forwarding tables for observation: %w", err)
	}
	var table *nftables.Table
	for _, candidate := range tables {
		if candidate.Name == forwardingTableName(desired.GatewayName) {
			table = candidate
			break
		}
	}
	markers := map[string]struct{}{}
	if table != nil {
		chains, listErr := conn.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
		if listErr != nil {
			return nil, fmt.Errorf("list gateway forwarding chains for observation: %w", listErr)
		}
		for _, chain := range chains {
			if chain.Table == nil || chain.Table.Name != table.Name {
				continue
			}
			rules, rulesErr := conn.GetRules(table, chain)
			if rulesErr != nil {
				return nil, fmt.Errorf("list gateway forwarding rules for observation: %w", rulesErr)
			}
			for _, rule := range rules {
				markers[chain.Name+"\x00"+string(rule.UserData)] = struct{}{}
			}
		}
	}
	for _, lease := range sortedPortForwardLeases(desired.PortForwardLeases) {
		observation := PortForwardRuleObservation{Identity: lease.Identity, LeaseGeneration: lease.LeaseGeneration, TargetAddress: lease.TargetAddress, TargetPort: lease.TargetPort, Ready: lease.LeaseGeneration > 0}
		for _, protocol := range sortedProtocols(lease.Protocols) {
			for kind, chain := range map[string]string{"dnat": "prerouting", "forward": "forward"} {
				if _, exists := markers[chain+"\x00"+string(portForwardRuleMarker(kind, lease, protocol))]; !exists {
					observation.Ready = false
				}
			}
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func ensureIPv4Forwarding() error {
	const path = "/proc/sys/net/ipv4/ip_forward"
	value, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read IPv4 forwarding state: %w", err)
	}
	if strings.TrimSpace(string(value)) == "1" {
		return nil
	}
	if err := os.WriteFile(path, []byte("1\n"), 0); err != nil {
		return fmt.Errorf("enable IPv4 forwarding: %w", err)
	}
	return nil
}

func readyLink(name string) (netlink.Link, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, err
	}
	if link.Attrs().Flags&net.FlagUp == 0 {
		return nil, fmt.Errorf("interface %q is down", name)
	}
	return link, nil
}

func replaceForwardingPolicy(desired DesiredState, overlayName, tunnelName string) error {
	conn := &nftables.Conn{}
	tableName := forwardingTableName(desired.GatewayName)
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return fmt.Errorf("list gateway forwarding tables: %w", err)
	}
	for _, existing := range tables {
		if existing.Name == tableName {
			conn.DelTable(existing)
		}
	}
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: tableName})
	drop := nftables.ChainPolicyDrop
	forward := conn.AddChain(&nftables.Chain{Table: table, Name: "forward", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &drop})
	prerouting := conn.AddChain(&nftables.Chain{Table: table, Name: "prerouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest})
	marker := []byte("waycloak-gateway:" + desired.GatewayName)
	prefix := netip.MustParsePrefix(desired.OverlayCIDR).Masked()
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: marker, Exprs: append([]expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED), Xor: binaryutil.NativeEndian.PutUint32(0)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
	}, append(destinationPrefixExpressions(prefix),
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(tunnelName)},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(overlayName)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	)...)})
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: marker, Exprs: append(sourcePrefixExpressions(prefix),
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(overlayName)},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(tunnelName)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	)})
	for _, lease := range sortedPortForwardLeases(desired.PortForwardLeases) {
		if lease.LeaseGeneration == 0 {
			continue
		}
		target := netip.MustParseAddr(lease.TargetAddress)
		for _, protocol := range sortedProtocols(lease.Protocols) {
			conn.AddRule(&nftables.Rule{Table: table, Chain: prerouting, UserData: portForwardRuleMarker("dnat", lease, protocol), Exprs: portForwardDNATExpressions(tunnelName, lease, protocol, target)})
			conn.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: portForwardRuleMarker("forward", lease, protocol), Exprs: portForwardAcceptExpressions(tunnelName, overlayName, lease, protocol, target)})
		}
	}
	postrouting := conn.AddChain(&nftables.Chain{Table: table, Name: "postrouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
	conn.AddRule(&nftables.Rule{Table: table, Chain: postrouting, UserData: marker, Exprs: append(sourcePrefixExpressions(prefix),
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(tunnelName)},
		&expr.Masq{},
	)})
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("replace gateway forwarding policy: %w", err)
	}
	return nil
}

func portForwardDNATExpressions(tunnelName string, lease PortForwardLeaseIntent, protocol provider.PortForwardProtocol, target netip.Addr) []expr.Any {
	return append(portForwardMatchExpressions(tunnelName, "", lease.InternalPort, protocol, netip.Addr{}),
		&expr.Immediate{Register: 1, Data: target.AsSlice()},
		&expr.Immediate{Register: 2, Data: encodedPort(lease.TargetPort)},
		&expr.NAT{Type: expr.NATTypeDestNAT, Family: unix.NFPROTO_IPV4, RegAddrMin: 1, RegProtoMin: 2},
	)
}

func portForwardAcceptExpressions(tunnelName, overlayName string, lease PortForwardLeaseIntent, protocol provider.PortForwardProtocol, target netip.Addr) []expr.Any {
	return append(portForwardMatchExpressions(tunnelName, overlayName, lease.TargetPort, protocol, target), &expr.Verdict{Kind: expr.VerdictAccept})
}

func portForwardMatchExpressions(inputName, outputName string, destinationPort uint16, protocol provider.PortForwardProtocol, destination netip.Addr) []expr.Any {
	protocolNumber := byte(unix.IPPROTO_TCP)
	if protocol == provider.ProtocolUDP {
		protocolNumber = unix.IPPROTO_UDP
	}
	expressions := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(inputName)},
	}
	if outputName != "" {
		expressions = append(expressions,
			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(outputName)},
		)
	}
	expressions = append(expressions,
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocolNumber}},
	)
	if destination.IsValid() {
		expressions = append(expressions,
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: destination.AsSlice()},
		)
	}
	return append(expressions,
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: encodedPort(destinationPort)},
	)
}

func encodedPort(port uint16) []byte {
	data := make([]byte, 2)
	binary.BigEndian.PutUint16(data, port)
	return data
}

func sortedPortForwardLeases(leases []PortForwardLeaseIntent) []PortForwardLeaseIntent {
	result := append([]PortForwardLeaseIntent(nil), leases...)
	sort.Slice(result, func(i, j int) bool { return result[i].Identity < result[j].Identity })
	return result
}

func sortedProtocols(protocols []provider.PortForwardProtocol) []provider.PortForwardProtocol {
	result := append([]provider.PortForwardProtocol(nil), protocols...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func portForwardRuleMarker(kind string, lease PortForwardLeaseIntent, protocol provider.PortForwardProtocol) []byte {
	return []byte(fmt.Sprintf("waycloak-port-forward:%s:%s:%d:%s:%s:%d", kind, lease.Identity, lease.LeaseGeneration, protocol, lease.TargetAddress, lease.TargetPort))
}

func ensureForwardingLockdown(gatewayName string) error {
	conn := &nftables.Conn{}
	tableName := forwardingTableName(gatewayName)
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return fmt.Errorf("list gateway forwarding tables: %w", err)
	}
	var existing *nftables.Table
	for _, table := range tables {
		if table.Name == tableName {
			existing = table
			break
		}
	}
	if existing != nil {
		chains, listErr := conn.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
		if listErr != nil {
			return fmt.Errorf("list gateway forwarding chains: %w", listErr)
		}
		for _, chain := range chains {
			if chain.Table != nil && chain.Table.Name == tableName && chain.Name == "forward" && chain.Policy != nil && *chain.Policy == nftables.ChainPolicyDrop {
				return nil
			}
		}
		conn.DelTable(existing)
	}
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: tableName})
	drop := nftables.ChainPolicyDrop
	conn.AddChain(&nftables.Chain{Table: table, Name: "forward", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &drop})
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("install gateway forwarding lockdown: %w", err)
	}
	return nil
}

func ensureVXLANIngressLockdown(gatewayName string, port int) error {
	conn := &nftables.Conn{}
	tableName := vxlanIngressTableName(gatewayName)
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("list gateway VXLAN ingress tables: %w", err)
	}
	var existing *nftables.Table
	for _, table := range tables {
		if table.Name == tableName {
			existing = table
			break
		}
	}
	if existing != nil {
		chains, listErr := conn.ListChainsOfTableFamily(nftables.TableFamilyINet)
		if listErr != nil {
			return fmt.Errorf("list gateway VXLAN ingress chains: %w", listErr)
		}
		for _, chain := range chains {
			if chain.Table == nil || chain.Table.Name != tableName || chain.Name != "input" {
				continue
			}
			rules, rulesErr := conn.GetRules(existing, chain)
			if rulesErr != nil {
				return fmt.Errorf("list gateway VXLAN ingress rules: %w", rulesErr)
			}
			for _, rule := range rules {
				if bytes.Equal(rule.UserData, vxlanDropMarker(gatewayName)) {
					return nil
				}
			}
		}
		conn.DelTable(existing)
	}
	table, chain := addVXLANIngressBase(conn, gatewayName)
	addVXLANIngressDrop(conn, table, chain, gatewayName, port)
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("install gateway VXLAN ingress lockdown: %w", err)
	}
	return nil
}

func replaceVXLANIngressPolicy(desired DesiredState) error {
	conn := &nftables.Conn{}
	tableName := vxlanIngressTableName(desired.GatewayName)
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("list gateway VXLAN ingress tables: %w", err)
	}
	for _, existing := range tables {
		if existing.Name == tableName {
			conn.DelTable(existing)
		}
	}
	table, chain := addVXLANIngressBase(conn, desired.GatewayName)
	marker := []byte("waycloak-vxlan-ingress:" + desired.GatewayName)
	for _, member := range desired.Members {
		underlay := netip.MustParseAddr(member.UnderlayIP).Unmap()
		expressions := append(vxlanIngressExpressions(desired.VXLANPort), sourceAddressExpressions(underlay)...)
		expressions = append(expressions, &expr.Verdict{Kind: expr.VerdictAccept})
		conn.AddRule(&nftables.Rule{Table: table, Chain: chain, UserData: marker, Exprs: expressions})
	}
	addVXLANIngressDrop(conn, table, chain, desired.GatewayName, desired.VXLANPort)
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("replace gateway VXLAN ingress policy: %w", err)
	}
	return nil
}

func addVXLANIngressBase(conn *nftables.Conn, gatewayName string) (*nftables.Table, *nftables.Chain) {
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: vxlanIngressTableName(gatewayName)})
	priority := nftables.ChainPriority(-1)
	accept := nftables.ChainPolicyAccept
	chain := conn.AddChain(&nftables.Chain{Table: table, Name: "input", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookInput, Priority: &priority, Policy: &accept})
	return table, chain
}

func addVXLANIngressDrop(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, gatewayName string, port int) {
	conn.AddRule(&nftables.Rule{Table: table, Chain: chain, UserData: vxlanDropMarker(gatewayName), Exprs: append(vxlanIngressExpressions(port), &expr.Verdict{Kind: expr.VerdictDrop})})
}

func vxlanIngressExpressions(port int) []expr.Any {
	encodedPort := make([]byte, 2)
	binary.BigEndian.PutUint16(encodedPort, uint16(port))
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: encodedPort},
	}
}

func sourceAddressExpressions(address netip.Addr) []expr.Any {
	protocol, offset := byte(unix.NFPROTO_IPV4), uint32(12)
	if address.Is6() {
		protocol, offset = byte(unix.NFPROTO_IPV6), 8
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: uint32(len(address.AsSlice()))},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: address.AsSlice()},
	}
}

func vxlanDropMarker(gatewayName string) []byte {
	return []byte("waycloak-vxlan-ingress:" + gatewayName + ":drop")
}

func sourcePrefixExpressions(prefix netip.Prefix) []expr.Any {
	return addressPrefixExpressions(prefix, 12)
}

func destinationPrefixExpressions(prefix netip.Prefix) []expr.Any {
	return addressPrefixExpressions(prefix, 16)
}

func addressPrefixExpressions(prefix netip.Prefix, offset uint32) []expr.Any {
	mask := net.CIDRMask(prefix.Bits(), 32)
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: prefix.Addr().AsSlice()},
	}
}

func fixedInterfaceName(value string) []byte {
	result := make([]byte, 16)
	copy(result, value)
	return result
}

func forwardingTableName(gatewayName string) string {
	return "waycloak_gw_" + strings.TrimPrefix(OverlayInterfaceName(gatewayName), "wcg")
}

func vxlanIngressTableName(gatewayName string) string {
	return "waycloak_vx_" + strings.TrimPrefix(OverlayInterfaceName(gatewayName), "wcg")
}
