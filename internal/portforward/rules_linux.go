// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package portforward

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/gatewaycontract"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const gatewayRuleTable = "waycloak_port_forward"

type LinuxRuleBackend struct {
	TunnelInterface  string
	OverlayInterface string
}

func (b LinuxRuleBackend) Replace(_ context.Context, rules []GatewayRule) error {
	if err := b.validate(); err != nil {
		return err
	}
	ordered := append([]GatewayRule(nil), rules...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].LeaseUID < ordered[j].LeaseUID })
	seen := map[wayv1.ObjectUID]struct{}{}
	for _, rule := range ordered {
		if _, exists := seen[rule.LeaseUID]; exists {
			return errors.New("duplicate lease UID in atomic gateway rule set")
		}
		seen[rule.LeaseUID] = struct{}{}
		if err := validateGatewayRule(rule); err != nil {
			return err
		}
	}
	conn := &nftables.Conn{}
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return fmt.Errorf("list gateway nftables state: %w", err)
	}
	for _, table := range tables {
		if table.Name == gatewayRuleTable {
			conn.DelTable(table)
		}
	}
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: gatewayRuleTable})
	prerouting := conn.AddChain(&nftables.Chain{Table: table, Name: "prerouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest})
	forward := conn.AddChain(&nftables.Chain{Table: table, Name: "forward", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter})
	postrouting := conn.AddChain(&nftables.Chain{Table: table, Name: "postrouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
	for _, rule := range ordered {
		for _, protocol := range rule.Protocols {
			addGatewayLeaseRules(conn, table, prerouting, forward, postrouting, b, rule, protocol)
		}
	}
	// Traffic arriving from the VPN tunnel that did not match an exact active
	// lease is denied. Other forwarding remains outside this owned table.
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: []byte("waycloak:unmatched-tunnel:drop"), Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes(b.TunnelInterface)},
		&expr.Verdict{Kind: expr.VerdictDrop},
	}})
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("replace atomic gateway port-forward rules: %w", err)
	}
	return nil
}

func (b LinuxRuleBackend) Ready(_ context.Context, rule GatewayRule) (bool, error) {
	if err := b.validate(); err != nil {
		return false, err
	}
	if err := validateGatewayRule(rule); err != nil {
		return false, err
	}
	want := map[string]struct{}{}
	for _, protocol := range rule.Protocols {
		for _, component := range []string{"dnat", "forward", "snat"} {
			want[gatewayMarker(rule, protocol, component)] = struct{}{}
		}
	}
	markers, err := gatewayRuleMarkers()
	if err != nil {
		return false, err
	}
	for marker := range want {
		if _, exists := markers[marker]; !exists {
			return false, nil
		}
	}
	return true, nil
}

func (LinuxRuleBackend) Withdrawn(_ context.Context, leaseUID wayv1.ObjectUID, generation int64) (bool, error) {
	markers, err := gatewayRuleMarkers()
	if err != nil {
		return false, err
	}
	prefix := fmt.Sprintf("waycloak:%s:%d:", leaseUID, generation)
	for marker := range markers {
		if strings.HasPrefix(marker, prefix) {
			return false, nil
		}
	}
	return true, nil
}

func addGatewayLeaseRules(conn *nftables.Conn, table *nftables.Table, prerouting, forward, postrouting *nftables.Chain, backend LinuxRuleBackend, rule GatewayRule, protocol wayv1.TransportProtocol) {
	transport := byte(unix.IPPROTO_TCP)
	if protocol == wayv1.ProtocolUDP {
		transport = unix.IPPROTO_UDP
	}
	ingressPort := portBytes(rule.IngressPort)
	targetPort := portBytes(rule.TargetPort)
	target := netip.MustParseAddr(rule.OverlayAddress).AsSlice()
	baseMatch := func() []expr.Any {
		return []expr.Any{
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes(backend.TunnelInterface)},
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{transport}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ingressPort},
		}
	}
	dnat := append(baseMatch(),
		&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(gatewaycontract.PortForwardIngressMark)},
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
		&expr.Immediate{Register: 1, Data: target}, &expr.Immediate{Register: 2, Data: targetPort},
		&expr.NAT{Type: expr.NATTypeDestNAT, Family: unix.NFPROTO_IPV4, RegAddrMin: 1, RegProtoMin: 2})
	conn.AddRule(&nftables.Rule{Table: table, Chain: prerouting, UserData: []byte(gatewayMarker(rule, protocol, "dnat")), Exprs: dnat})
	// Forward sees the packet after prerouting DNAT, so it must match the exact
	// translated Pod address and Service target port. Matching the provider
	// port here would make every legitimate packet fall through to the
	// unmatched-tunnel drop rule.
	forwardMatch := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes(backend.TunnelInterface)},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes(backend.OverlayInterface)},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{transport}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: target},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: targetPort},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: []byte(gatewayMarker(rule, protocol, "forward")), Exprs: forwardMatch})
	returnMatch := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes(backend.OverlayInterface)},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes(backend.TunnelInterface)},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: target},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{transport}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 2}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: targetPort},
		&expr.Immediate{Register: 1, Data: ingressPort}, &expr.Masq{ToPorts: true, RegProtoMin: 1},
	}
	conn.AddRule(&nftables.Rule{Table: table, Chain: postrouting, UserData: []byte(gatewayMarker(rule, protocol, "snat")), Exprs: returnMatch})
}

func gatewayRuleMarkers() (map[string]struct{}, error) {
	conn := &nftables.Conn{}
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return nil, err
	}
	var table *nftables.Table
	for _, candidate := range tables {
		if candidate.Name == gatewayRuleTable {
			table = candidate
			break
		}
	}
	if table == nil {
		return map[string]struct{}{}, nil
	}
	chains, err := conn.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return nil, err
	}
	markers := map[string]struct{}{}
	for _, chain := range chains {
		if chain.Table == nil || chain.Table.Name != table.Name {
			continue
		}
		rules, rulesErr := conn.GetRules(table, chain)
		if rulesErr != nil {
			return nil, rulesErr
		}
		for _, rule := range rules {
			markers[string(rule.UserData)] = struct{}{}
		}
	}
	return markers, nil
}

func validateGatewayRule(rule GatewayRule) error {
	address, err := netip.ParseAddr(rule.OverlayAddress)
	if rule.LeaseUID == "" || rule.HandoffGeneration < 1 || rule.IngressPort == 0 || rule.TargetPort == 0 || err != nil || !address.Is4() || len(rule.Protocols) == 0 {
		return errors.New("gateway port-forward rule is invalid")
	}
	for _, protocol := range rule.Protocols {
		if protocol != wayv1.ProtocolTCP && protocol != wayv1.ProtocolUDP {
			return errors.New("gateway port-forward protocol is unsupported")
		}
	}
	return nil
}

func (b LinuxRuleBackend) validate() error {
	if b.TunnelInterface == "" || b.OverlayInterface == "" || len(b.TunnelInterface) > 15 || len(b.OverlayInterface) > 15 || b.TunnelInterface == b.OverlayInterface {
		return errors.New("exact distinct tunnel and overlay interfaces are required")
	}
	return nil
}

func gatewayMarker(rule GatewayRule, protocol wayv1.TransportProtocol, component string) string {
	return fmt.Sprintf("waycloak:%s:%d:%s:%s", rule.LeaseUID, rule.HandoffGeneration, strings.ToLower(string(protocol)), component)
}

func portBytes(port uint16) []byte {
	value := make([]byte, 2)
	binary.BigEndian.PutUint16(value, port)
	return value
}

func interfaceNameBytes(value string) []byte {
	result := make([]byte, 16)
	copy(result, value)
	return result
}

var _ RuleBackend = LinuxRuleBackend{}
