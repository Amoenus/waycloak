// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package dataplane

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	outputChainName       = "output"
	waycloakRouteProtocol = netlink.RouteProtocol(99)
	waycloakRuleProtocol  = uint8(99)
	protectedRouteTable   = 51820
	endpointRulePriority  = 11000
	dnsUDPRulePriority    = 11050
	dnsTCPRulePriority    = 11051
	clusterRulePriority   = 11100
	protectedRulePriority = 12000
)

type linuxBackend struct{}

func NewBackend() Backend { return &linuxBackend{} }

func (*linuxBackend) Preflight(context.Context) error {
	conn := &nftables.Conn{}
	if _, err := conn.ListTables(); err != nil {
		return fmt.Errorf("query nftables (NET_ADMIN and nf_tables are required): %w", err)
	}
	if _, err := netlink.LinkList(); err != nil {
		return fmt.Errorf("query netlink links: %w", err)
	}
	return nil
}

func (*linuxBackend) InstallLockdown(_ context.Context, podUID string) error {
	if podUID == "" {
		return errors.New("pod UID is required")
	}
	return replacePolicy(podUID, "", netip.AddrPort{}, "", netip.Addr{}, netip.Addr{}, ClusterTrafficGateway, nil, nil)
}

func (*linuxBackend) Configure(_ context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	linkName := overlayName(cfg)
	underlay, route, err := resolveUnderlay(cfg)
	if err != nil {
		return err
	}
	vxlan, err := ensureVXLAN(cfg, linkName, underlay, route)
	if err != nil {
		return err
	}
	if err := ensureOverlayAddress(vxlan, cfg.Address); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(vxlan); err != nil {
		return fmt.Errorf("bring overlay link up: %w", err)
	}
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: vxlan.Attrs().Index, Gw: net.IP(cfg.GatewayAddress.AsSlice()), Table: protectedRouteTable, Protocol: waycloakRouteProtocol}); err != nil {
		return fmt.Errorf("install protected default route: %w", err)
	}
	if err := reconcilePolicyRules(cfg); err != nil {
		return fmt.Errorf("reconcile protected policy-routing rules: %w", err)
	}
	if err := replacePolicy(cfg.PodUID, underlay.Attrs().Name, cfg.GatewayEndpoint, linkName, cfg.GatewayAddress, cfg.Address.Addr(), cfg.ClusterTrafficMode, cfg.ClusterCIDRs, cfg.ApplicationPortRedirects); err != nil {
		return fmt.Errorf("activate protected nftables policy: %w", err)
	}
	return nil
}

func (*linuxBackend) Verify(ctx context.Context, cfg Config) error {
	if err := verifyPolicy(cfg.PodUID, true); err != nil {
		return err
	}
	link, err := netlink.LinkByName(overlayName(cfg))
	if err != nil {
		return fmt.Errorf("find overlay link: %w", err)
	}
	vxlan, ok := link.(*netlink.Vxlan)
	if !ok || vxlan.VxlanId != int(cfg.VNI) {
		return errors.New("overlay link does not have the allocated VNI")
	}
	if link.Attrs().Flags&net.FlagUp == 0 {
		return errors.New("overlay link is down")
	}
	addrs, err := netlink.AddrList(link, familyFor(cfg.Address.Addr()))
	if err != nil {
		return fmt.Errorf("list overlay addresses: %w", err)
	}
	wantAddress := cfg.Address.Masked().Addr()
	foundAddress := false
	for _, address := range addrs {
		if parsed, ok := netip.AddrFromSlice(address.IP); ok && parsed.Unmap() == cfg.Address.Addr().Unmap() {
			foundAddress = true
			break
		}
	}
	if !foundAddress {
		return fmt.Errorf("overlay address %s is not installed (network %s)", cfg.Address, wantAddress)
	}
	routes, err := netlink.RouteListFiltered(familyFor(cfg.GatewayAddress), &netlink.Route{Table: protectedRouteTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("list default routes: %w", err)
	}
	for _, route := range routes {
		if route.LinkIndex == link.Attrs().Index && addrEqual(route.Gw, cfg.GatewayAddress) {
			return probeGatewayReadiness(ctx, netip.AddrPortFrom(cfg.GatewayAddress, cfg.GatewayHealthPort))
		}
	}
	return errors.New("protected default route is not installed")
}

func probeGatewayReadiness(ctx context.Context, endpoint netip.AddrPort) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint.String()+"/readyz", nil)
	if err != nil {
		return fmt.Errorf("build gateway readiness probe: %w", err)
	}
	client := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("probe observed gateway readiness endpoint: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway readiness endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (b *linuxBackend) Repair(ctx context.Context, cfg Config) error {
	if err := b.Configure(ctx, cfg); err != nil {
		return err
	}
	return b.Verify(ctx, cfg)
}

func replacePolicy(podUID, underlayName string, endpoint netip.AddrPort, overlayName string, dnsGateway, applicationAddress netip.Addr, mode ClusterTrafficMode, clusterCIDRs []netip.Prefix, redirects []ApplicationPortRedirect) error {
	conn := &nftables.Conn{}
	tableName := policyTableName(podUID)
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return err
	}
	for _, existing := range tables {
		if existing.Name == tableName {
			conn.DelTable(existing)
		}
	}
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: tableName})
	policy := nftables.ChainPolicyDrop
	chain := conn.AddChain(&nftables.Chain{Table: table, Name: outputChainName, Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityRaw, Policy: &policy})
	marker := []byte("waycloak:" + podUID)
	addInterfaceVerdict(conn, table, chain, "lo", expr.VerdictAccept, marker)
	if mode == ClusterTrafficDeny {
		for _, prefix := range clusterCIDRs {
			addPrefixVerdict(conn, table, chain, prefix, expr.VerdictDrop, marker)
		}
	}
	if overlayName != "" {
		addInterfaceVerdict(conn, table, chain, overlayName, expr.VerdictAccept, marker)
	}
	if underlayName != "" && endpoint.IsValid() {
		addEndpointVerdict(conn, table, chain, underlayName, endpoint, marker)
	}
	if mode == ClusterTrafficPreserve {
		for _, prefix := range clusterCIDRs {
			addPrefixVerdict(conn, table, chain, prefix, expr.VerdictAccept, marker)
		}
	}
	if dnsGateway.IsValid() {
		addDNSRedirect(conn, table, dnsGateway, marker)
	}
	if len(redirects) > 0 {
		addApplicationPortRedirects(conn, table, applicationAddress, redirects, marker)
	}
	return conn.Flush()
}

func addApplicationPortRedirects(conn *nftables.Conn, table *nftables.Table, address netip.Addr, redirects []ApplicationPortRedirect, marker []byte) {
	chain := conn.AddChain(&nftables.Chain{Table: table, Name: "application-port-prerouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest})
	address = address.Unmap()
	family := uint32(unix.NFPROTO_IPV4)
	nfproto := byte(unix.NFPROTO_IPV4)
	offset := uint32(16)
	if address.Is6() {
		family = unix.NFPROTO_IPV6
		nfproto = byte(unix.NFPROTO_IPV6)
		offset = 24
	}
	for _, redirect := range redirects {
		from := make([]byte, 2)
		binary.BigEndian.PutUint16(from, redirect.TargetPort)
		to := make([]byte, 2)
		binary.BigEndian.PutUint16(to, redirect.ApplicationPort)
		for _, protocol := range redirect.Protocols {
			transport := byte(unix.IPPROTO_TCP)
			if protocol == "UDP" {
				transport = unix.IPPROTO_UDP
			}
			conn.AddRule(&nftables.Rule{Table: table, Chain: chain, UserData: append(append([]byte(nil), marker...), []byte(":"+redirect.Identity)...), Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{nfproto}},
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: uint32(len(address.AsSlice()))},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: address.AsSlice()},
				&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{transport}},
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: from},
				&expr.Immediate{Register: 1, Data: address.AsSlice()},
				&expr.Immediate{Register: 2, Data: to},
				&expr.NAT{Type: expr.NATTypeDestNAT, Family: family, RegAddrMin: 1, RegProtoMin: 2},
			}})
		}
	}
}

func addDNSRedirect(conn *nftables.Conn, table *nftables.Table, gateway netip.Addr, marker []byte) {
	chain := conn.AddChain(&nftables.Chain{Table: table, Name: "dns-output", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest})
	gateway = gateway.Unmap()
	family := uint32(unix.NFPROTO_IPV4)
	protocol := byte(unix.NFPROTO_IPV4)
	if gateway.Is6() {
		family = unix.NFPROTO_IPV6
		protocol = byte(unix.NFPROTO_IPV6)
	}
	queryPort := make([]byte, 2)
	binary.BigEndian.PutUint16(queryPort, 53)
	gatewayPort := make([]byte, 2)
	binary.BigEndian.PutUint16(gatewayPort, contract.GatewayDNSPort)
	for _, transport := range []byte{unix.IPPROTO_UDP, unix.IPPROTO_TCP} {
		conn.AddRule(&nftables.Rule{Table: table, Chain: chain, UserData: marker, Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}},
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{transport}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: queryPort},
			&expr.Immediate{Register: 1, Data: gateway.AsSlice()},
			&expr.Immediate{Register: 2, Data: gatewayPort},
			&expr.NAT{Type: expr.NATTypeDestNAT, Family: family, RegAddrMin: 1, RegProtoMin: 2},
		}})
	}
}

func addInterfaceVerdict(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, name string, verdict expr.VerdictKind, marker []byte) {
	conn.AddRule(&nftables.Rule{Table: table, Chain: chain, UserData: marker, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceName(name)},
		&expr.Verdict{Kind: verdict},
	}})
}

func addEndpointVerdict(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, underlay string, endpoint netip.AddrPort, marker []byte) {
	addr := endpoint.Addr().Unmap()
	offset, protocol := uint32(16), byte(unix.NFPROTO_IPV4)
	if addr.Is6() {
		offset, protocol = 24, byte(unix.NFPROTO_IPV6)
	}
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, endpoint.Port())
	conn.AddRule(&nftables.Rule{Table: table, Chain: chain, UserData: marker, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceName(underlay)},
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: uint32(len(addr.AsSlice()))},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr.AsSlice()},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: port},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}})
}

func addPrefixVerdict(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, prefix netip.Prefix, verdict expr.VerdictKind, marker []byte) {
	prefix = prefix.Masked()
	addr := prefix.Addr().Unmap()
	offset, protocol, bits := uint32(16), byte(unix.NFPROTO_IPV4), 32
	if addr.Is6() {
		offset, protocol, bits = 24, byte(unix.NFPROTO_IPV6), 128
	}
	mask := net.CIDRMask(prefix.Bits(), bits)
	conn.AddRule(&nftables.Rule{Table: table, Chain: chain, UserData: marker, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: uint32(len(addr.AsSlice()))},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: uint32(len(mask)), Mask: mask, Xor: make([]byte, len(mask))},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr.AsSlice()},
		&expr.Verdict{Kind: verdict},
	}})
}

func verifyPolicy(podUID string, requireDNS bool) error {
	conn := &nftables.Conn{}
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("list nftables policy: %w", err)
	}
	var table *nftables.Table
	for _, candidate := range tables {
		if candidate.Name == policyTableName(podUID) {
			table = candidate
			break
		}
	}
	if table == nil {
		return errors.New("owned nftables table is missing")
	}
	chains, err := conn.ListChainsOfTableFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("list nftables chains: %w", err)
	}
	filterReady, dnsReady := false, !requireDNS
	for _, chain := range chains {
		if chain.Table != nil && chain.Table.Name == table.Name && chain.Name == outputChainName && chain.Policy != nil && *chain.Policy == nftables.ChainPolicyDrop {
			filterReady = true
		}
		if chain.Table != nil && chain.Table.Name == table.Name && chain.Name == "dns-output" && chain.Type == nftables.ChainTypeNAT {
			dnsReady = true
		}
	}
	if !filterReady {
		return errors.New("owned nftables output-drop chain is missing")
	}
	if !dnsReady {
		return errors.New("owned gateway DNS redirect chain is missing")
	}
	return nil
}

func resolveUnderlay(cfg Config) (netlink.Link, netlink.Route, error) {
	routes, err := netlink.RouteGet(net.IP(cfg.GatewayEndpoint.Addr().AsSlice()))
	if err != nil || len(routes) == 0 {
		return nil, netlink.Route{}, fmt.Errorf("resolve gateway underlay route: %w", err)
	}
	route := routes[0]
	var link netlink.Link
	if cfg.UnderlayInterface != "" {
		link, err = netlink.LinkByName(cfg.UnderlayInterface)
	} else {
		link, err = netlink.LinkByIndex(route.LinkIndex)
	}
	if err != nil {
		return nil, netlink.Route{}, fmt.Errorf("resolve gateway underlay link: %w", err)
	}
	if route.LinkIndex != link.Attrs().Index {
		return nil, netlink.Route{}, fmt.Errorf("configured underlay %q does not carry the gateway endpoint route", link.Attrs().Name)
	}
	return link, route, nil
}

func reconcilePolicyRules(cfg Config) error {
	family := familyFor(cfg.GatewayEndpoint.Addr())
	rules, err := netlink.RuleList(family)
	if err != nil {
		return err
	}
	for i := range rules {
		if rules[i].Protocol == waycloakRuleProtocol {
			if err := netlink.RuleDel(&rules[i]); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
		}
	}
	endpoint := cfg.GatewayEndpoint.Addr().Unmap()
	if err := addPolicyRule(family, endpointRulePriority, unix.RT_TABLE_MAIN, netip.PrefixFrom(endpoint, endpoint.BitLen())); err != nil {
		return err
	}
	if err := addDNSPolicyRule(family, dnsUDPRulePriority, unix.IPPROTO_UDP); err != nil {
		return err
	}
	if err := addDNSPolicyRule(family, dnsTCPRulePriority, unix.IPPROTO_TCP); err != nil {
		return err
	}
	if cfg.ClusterTrafficMode == ClusterTrafficPreserve {
		priority := clusterRulePriority
		for _, prefix := range cfg.ClusterCIDRs {
			if prefix.Addr().Is6() != endpoint.Is6() {
				continue
			}
			if err := addPolicyRule(family, priority, unix.RT_TABLE_MAIN, prefix); err != nil {
				return err
			}
			priority++
		}
	}
	rule := netlink.NewRule()
	rule.Family = family
	rule.Priority = protectedRulePriority
	rule.Table = protectedRouteTable
	rule.Protocol = waycloakRuleProtocol
	if err := netlink.RuleAdd(rule); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	return nil
}

func addDNSPolicyRule(family, priority, protocol int) error {
	rule := netlink.NewRule()
	rule.Family = family
	rule.Priority = priority
	rule.Table = protectedRouteTable
	rule.Protocol = waycloakRuleProtocol
	rule.IPProto = protocol
	rule.Dport = netlink.NewRulePortRange(53, 53)
	if err := netlink.RuleAdd(rule); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	return nil
}

func addPolicyRule(family, priority, table int, prefix netip.Prefix) error {
	prefix = prefix.Masked()
	rule := netlink.NewRule()
	rule.Family = family
	rule.Priority = priority
	rule.Table = table
	rule.Protocol = waycloakRuleProtocol
	rule.Dst = &net.IPNet{IP: net.IP(prefix.Addr().AsSlice()), Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen())}
	if err := netlink.RuleAdd(rule); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	return nil
}

func ensureVXLAN(cfg Config, name string, underlay netlink.Link, route netlink.Route) (netlink.Link, error) {
	if existing, err := netlink.LinkByName(name); err == nil {
		vxlan, ok := existing.(*netlink.Vxlan)
		if !ok || vxlan.VxlanId != int(cfg.VNI) || vxlan.Port != int(cfg.GatewayEndpoint.Port()) {
			return nil, fmt.Errorf("owned overlay link %q has conflicting attributes", name)
		}
		return existing, nil
	}
	source := route.Src
	if len(source) == 0 {
		addrs, err := netlink.AddrList(underlay, familyFor(cfg.GatewayEndpoint.Addr()))
		if err != nil || len(addrs) == 0 {
			return nil, fmt.Errorf("discover underlay source address: %w", err)
		}
		source = addrs[0].IP
	}
	vxlan := &netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: name, MTU: cfg.MTU}, VxlanId: int(cfg.VNI), VtepDevIndex: underlay.Attrs().Index, SrcAddr: source, Group: net.IP(cfg.GatewayEndpoint.Addr().AsSlice()), Port: int(cfg.GatewayEndpoint.Port()), Learning: false, NoAge: true}
	if err := netlink.LinkAdd(vxlan); err != nil {
		return nil, fmt.Errorf("create VXLAN overlay: %w", err)
	}
	return vxlan, nil
}

func ensureOverlayAddress(link netlink.Link, prefix netip.Prefix) error {
	address := &netlink.Addr{IPNet: &net.IPNet{IP: net.IP(prefix.Addr().AsSlice()), Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen())}}
	if err := netlink.AddrReplace(link, address); err != nil {
		return fmt.Errorf("configure overlay address: %w", err)
	}
	return nil
}

func overlayName(cfg Config) string {
	if cfg.OverlayInterfaceName != "" {
		return cfg.OverlayInterfaceName
	}
	sum := sha256.Sum256([]byte(cfg.PodUID))
	return fmt.Sprintf("wc%x", sum[:6])
}

func policyTableName(podUID string) string {
	sum := sha256.Sum256([]byte(podUID))
	return fmt.Sprintf("waycloak_%x", sum[:8])
}

func interfaceName(value string) []byte {
	result := make([]byte, 16)
	copy(result, value)
	return result
}

func familyFor(address netip.Addr) int {
	if address.Is6() {
		return netlink.FAMILY_V6
	}
	return netlink.FAMILY_V4
}

func addrEqual(value net.IP, want netip.Addr) bool {
	parsed, ok := netip.AddrFromSlice(value)
	return ok && parsed.Unmap() == want.Unmap()
}
