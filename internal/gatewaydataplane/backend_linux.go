// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package gatewaydataplane

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const coreTableName = "waycloak_gateway_core"

const ipv4ForwardingPath = "/proc/sys/net/ipv4/ip_forward"

const (
	gatewayRouteProtocol = netlink.RouteProtocol(99)
	// Gluetun installs FIREWALL_OUTBOUND_SUBNETS policy routing at priority 99.
	// The overlay return path must win before that rule or replies are sent to
	// eth0 instead of the owned VXLAN interface.
	gatewayOverlayReturnPriority = 90
	gatewayClusterDNSPriority    = 89
	gatewayClusterDNSTable       = 198
	engineFilterTableName        = "filter"
	engineInputChainName         = "INPUT"
	engineForwardChainName       = "FORWARD"
	engineOutputChainName        = "OUTPUT"
	engineRuleMarkerPrefix       = "waycloak:gluetun:"
)

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
	if err := ensureOverlayReturnRule(config.OverlayCIDR); err != nil {
		return fmt.Errorf("install gateway overlay return-path rule: %w", err)
	}
	if err := ensureClusterDNSRoute(config, underlay); err != nil {
		return fmt.Errorf("install reviewed cluster-DNS route: %w", err)
	}
	if err := ensureClusterDNSRule(config.ClusterDNSUpstream.Addr()); err != nil {
		return fmt.Errorf("install reviewed cluster-DNS route rule: %w", err)
	}
	if err := requireIPv4Forwarding(ipv4ForwardingPath); err != nil {
		return err
	}
	createdLink = false
	return nil
}

func ensureClusterDNSRule(address netip.Addr) error {
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	exact := false
	for index := range rules {
		if rules[index].Priority == gatewayClusterDNSPriority && rules[index].Protocol == uint8(gatewayRouteProtocol) {
			if rules[index].Table == gatewayClusterDNSTable && rules[index].Dst != nil && rules[index].Dst.String() == address.String()+"/32" {
				exact = true
				continue
			}
			if err := netlink.RuleDel(&rules[index]); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
		}
	}
	if exact {
		return nil
	}
	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V4
	rule.Priority = gatewayClusterDNSPriority
	rule.Table = gatewayClusterDNSTable
	rule.Protocol = uint8(gatewayRouteProtocol)
	rule.Dst = netipPrefix(address, 32)
	if err := netlink.RuleAdd(rule); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	return nil
}

func ensureClusterDNSRoute(config Config, underlay netlink.Link) error {
	mainRoutes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: unix.RT_TABLE_MAIN, LinkIndex: underlay.Attrs().Index}, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_OIF)
	if err != nil {
		return err
	}
	gateways := map[string]net.IP{}
	for _, route := range mainRoutes {
		if route.Dst == nil && route.Gw != nil && route.Gw.To4() != nil {
			gateways[route.Gw.String()] = route.Gw
		}
	}
	if len(gateways) != 1 {
		return errors.New("underlay has no unique IPv4 default gateway")
	}
	var gateway net.IP
	for _, value := range gateways {
		gateway = value
	}
	destination := netipPrefix(config.ClusterDNSUpstream.Addr(), 32)
	existing, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: gatewayClusterDNSTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return err
	}
	exact := false
	for index := range existing {
		if existing[index].Protocol != gatewayRouteProtocol {
			continue
		}
		if existing[index].LinkIndex == underlay.Attrs().Index && existing[index].Dst != nil && existing[index].Dst.String() == destination.String() && existing[index].Gw.Equal(gateway) {
			exact = true
			continue
		}
		if err := netlink.RouteDel(&existing[index]); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	if exact {
		return nil
	}
	return netlink.RouteReplace(&netlink.Route{LinkIndex: underlay.Attrs().Index, Dst: destination, Gw: gateway, Table: gatewayClusterDNSTable, Protocol: gatewayRouteProtocol})
}

func ensureOverlayReturnRule(overlay netip.Prefix) error {
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for index := range rules {
		if rules[index].Priority == gatewayOverlayReturnPriority && rules[index].Protocol == uint8(gatewayRouteProtocol) {
			if err := netlink.RuleDel(&rules[index]); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
		}
	}
	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V4
	rule.Priority = gatewayOverlayReturnPriority
	rule.Table = unix.RT_TABLE_MAIN
	rule.Protocol = uint8(gatewayRouteProtocol)
	rule.Dst = netipPrefix(overlay.Masked().Addr(), overlay.Bits())
	if err := netlink.RuleAdd(rule); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
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
	current, err := gatewayRulesCurrent(connection, config, healthy)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
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
	if err := replaceEngineFilterRules(connection, config, healthy); err != nil {
		return err
	}
	if err := connection.Flush(); err != nil {
		return fmt.Errorf("replace gateway fail-closed rules: %w", err)
	}
	return nil
}

func gatewayRulesCurrent(connection *nftables.Conn, config Config, healthy bool) (bool, error) {
	tables, err := connection.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return false, err
	}
	chains, err := connection.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return false, err
	}
	var core, filter *nftables.Table
	for _, table := range tables {
		switch table.Name {
		case coreTableName:
			core = table
		case engineFilterTableName:
			filter = table
		}
	}
	if core == nil {
		return false, nil
	}
	var coreForward, corePostrouting, engineInput, engineForward, engineOutput *nftables.Chain
	for _, chain := range chains {
		if chain.Table == nil {
			continue
		}
		switch {
		case chain.Table.Name == coreTableName && chain.Name == "forward":
			coreForward = chain
		case chain.Table.Name == coreTableName && chain.Name == "postrouting":
			corePostrouting = chain
		case chain.Table.Name == engineFilterTableName && chain.Name == engineInputChainName:
			engineInput = chain
		case chain.Table.Name == engineFilterTableName && chain.Name == engineForwardChainName:
			engineForward = chain
		case chain.Table.Name == engineFilterTableName && chain.Name == engineOutputChainName:
			engineOutput = chain
		}
	}
	if coreForward == nil || corePostrouting == nil || coreForward.Policy == nil || *coreForward.Policy != nftables.ChainPolicyDrop {
		return false, nil
	}
	forwardMarkers, postroutingMarkers := []string{}, []string{}
	inputMarkers, engineForwardMarkers := []string{}, []string{}
	outputMarkers := []string{engineRuleMarkerPrefix + "cluster-dns-udp-output", engineRuleMarkerPrefix + "cluster-dns-tcp-output"}
	if healthy {
		forwardMarkers = []string{"waycloak:overlay-to-tunnel", "waycloak:tunnel-to-overlay-established"}
		postroutingMarkers = []string{"waycloak:overlay-masquerade"}
		inputMarkers = []string{engineRuleMarkerPrefix + "health-input", engineRuleMarkerPrefix + "dns-udp-input", engineRuleMarkerPrefix + "dns-tcp-input"}
		engineForwardMarkers = []string{engineRuleMarkerPrefix + "overlay-to-tunnel", engineRuleMarkerPrefix + "tunnel-to-overlay-established"}
	}
	for _, check := range []struct {
		table    *nftables.Table
		chain    *nftables.Chain
		prefix   string
		expected []string
		required bool
	}{
		{core, coreForward, "waycloak:", forwardMarkers, true},
		{core, corePostrouting, "waycloak:", postroutingMarkers, true},
		{filter, engineInput, engineRuleMarkerPrefix, inputMarkers, healthy},
		{filter, engineForward, engineRuleMarkerPrefix, engineForwardMarkers, healthy},
		{filter, engineOutput, engineRuleMarkerPrefix, outputMarkers, true},
	} {
		if check.table == nil || check.chain == nil {
			if check.required {
				return false, nil
			}
			continue
		}
		matches, err := exactRuleMarkers(connection, check.table, check.chain, check.prefix, check.expected)
		if err != nil || !matches {
			return false, err
		}
	}
	return true, nil
}

func exactRuleMarkers(connection *nftables.Conn, table *nftables.Table, chain *nftables.Chain, prefix string, expected []string) (bool, error) {
	rules, err := connection.GetRules(table, chain)
	if err != nil {
		return false, err
	}
	counts := map[string]int{}
	for _, rule := range rules {
		marker := string(rule.UserData)
		if strings.HasPrefix(marker, prefix) {
			counts[marker]++
		}
	}
	if len(counts) != len(expected) {
		return false, nil
	}
	for _, marker := range expected {
		if counts[marker] != 1 {
			return false, nil
		}
	}
	return true, nil
}

func replaceEngineFilterRules(connection *nftables.Conn, config Config, healthy bool) error {
	tables, err := connection.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return err
	}
	chains, err := connection.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return err
	}
	var table *nftables.Table
	for _, candidate := range tables {
		if candidate.Name == engineFilterTableName {
			table = candidate
			break
		}
	}
	if table == nil {
		if healthy {
			return errors.New("gluetun filter table is unavailable")
		}
		return nil
	}
	var input, forward, output *nftables.Chain
	for _, chain := range chains {
		if chain.Table == nil || chain.Table.Name != table.Name {
			continue
		}
		switch chain.Name {
		case engineInputChainName:
			input = chain
		case engineForwardChainName:
			forward = chain
		case engineOutputChainName:
			output = chain
		}
	}
	if output == nil || healthy && (input == nil || forward == nil) {
		return errors.New("required Gluetun filter chains are unavailable")
	}
	for _, chain := range []*nftables.Chain{input, forward, output} {
		if chain == nil {
			continue
		}
		rules, err := connection.GetRules(table, chain)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if strings.HasPrefix(string(rule.UserData), engineRuleMarkerPrefix) {
				if err := connection.DelRule(rule); err != nil {
					return err
				}
			}
		}
	}
	connection.AddRule(&nftables.Rule{Table: table, Chain: output, UserData: []byte(engineRuleMarkerPrefix + "cluster-dns-udp-output"), Exprs: outputDNS(config, unix.IPPROTO_UDP)})
	connection.AddRule(&nftables.Rule{Table: table, Chain: output, UserData: []byte(engineRuleMarkerPrefix + "cluster-dns-tcp-output"), Exprs: outputDNS(config, unix.IPPROTO_TCP)})
	if healthy {
		connection.AddRule(&nftables.Rule{Table: table, Chain: input, UserData: []byte(engineRuleMarkerPrefix + "health-input"), Exprs: inputPort(config.OverlayInterface, unix.IPPROTO_TCP, config.HealthPort)})
		connection.AddRule(&nftables.Rule{Table: table, Chain: input, UserData: []byte(engineRuleMarkerPrefix + "dns-udp-input"), Exprs: inputPort(config.OverlayInterface, unix.IPPROTO_UDP, DNSListenPort)})
		connection.AddRule(&nftables.Rule{Table: table, Chain: input, UserData: []byte(engineRuleMarkerPrefix + "dns-tcp-input"), Exprs: inputPort(config.OverlayInterface, unix.IPPROTO_TCP, DNSListenPort)})
		connection.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: []byte(engineRuleMarkerPrefix + "overlay-to-tunnel"), Exprs: interfacePair(config.OverlayInterface, config.TunnelInterface, true)})
		connection.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: []byte(engineRuleMarkerPrefix + "tunnel-to-overlay-established"), Exprs: append(interfacePair(config.TunnelInterface, config.OverlayInterface, false), established()...)})
	}
	return nil
}

func outputDNS(config Config, protocol byte) []expr.Any {
	encodedPort := make([]byte, 2)
	binary.BigEndian.PutUint16(encodedPort, config.ClusterDNSUpstream.Port())
	address := config.ClusterDNSUpstream.Addr().As4()
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceBytes(config.UnderlayInterface)},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: address[:]},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: encodedPort},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

func inputPort(input string, protocol byte, port uint16) []expr.Any {
	encodedPort := make([]byte, 2)
	binary.BigEndian.PutUint16(encodedPort, port)
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceBytes(input)},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: encodedPort},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
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
