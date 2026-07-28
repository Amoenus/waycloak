// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// fake-gluetun is a disposable conformance fixture. In gateway mode it exposes
// the Gluetun observation API and creates a kernel WireGuard tunnel named tun0.
// In exit mode it owns the other end and a narrow forwarding/NAT policy. It is
// not a supported VPN provider implementation.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	ownedAlias      = "waycloak:disposable-wireguard-fixture"
	exitTableName   = "waycloak_test_wg_exit"
	gatewayRouteTab = 51821
)

func main() {
	mode := flag.String("mode", "gateway", "fixture mode: gateway, exit, or keygen")
	flag.Parse()
	if *mode == "keygen" {
		privateKey, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			panic(err)
		}
		fmt.Printf("%s\n%s\n", privateKey.String(), privateKey.PublicKey().String())
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var err error
	switch *mode {
	case "gateway":
		err = runGateway(ctx)
	case "exit":
		err = runExit(ctx)
	default:
		err = fmt.Errorf("unsupported fixture mode %q", *mode)
	}
	if err != nil {
		panic(err)
	}
}

func runGateway(ctx context.Context) error {
	if value := strings.TrimSpace(os.Getenv("WIREGUARD_STARTUP_DELAY")); value != "" {
		delay, err := time.ParseDuration(value)
		if err != nil || delay < 0 || delay > 2*time.Minute {
			return errors.New("WIREGUARD_STARTUP_DELAY must be between zero and two minutes")
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
	endpoint, err := net.ResolveUDPAddr("udp4", requiredEnvironment("WIREGUARD_ENDPOINT"))
	if err != nil {
		return fmt.Errorf("resolve exit endpoint: %w", err)
	}
	privateKey, err := wgtypes.ParseKey(requiredEnvironment("OPENVPN_USER"))
	if err != nil {
		return errors.New("parse gateway fixture private key")
	}
	peerKey, err := wgtypes.ParseKey(requiredEnvironment("OPENVPN_PASSWORD"))
	if err != nil {
		return errors.New("parse exit fixture public key")
	}
	overlay, err := netip.ParsePrefix(requiredEnvironment("FIREWALL_OUTBOUND_SUBNETS"))
	if err != nil || !overlay.Addr().Is4() {
		return errors.New("parse gateway fixture overlay CIDR")
	}
	if err := configureWireGuard("tun0", "10.200.0.2/30", privateKey, peerKey, endpoint, mustPrefix("0.0.0.0/0"), nil); err != nil {
		return err
	}
	if err := installGatewayRoute(overlay.Masked()); err != nil {
		return err
	}
	return serveObservation(ctx)
}

func runExit(ctx context.Context) error {
	privateKey, err := wgtypes.ParseKey(requiredEnvironment("WIREGUARD_PRIVATE_KEY"))
	if err != nil {
		return errors.New("parse exit fixture private key")
	}
	peerKey, err := wgtypes.ParseKey(requiredEnvironment("WIREGUARD_PEER_PUBLIC_KEY"))
	if err != nil {
		return errors.New("parse gateway fixture public key")
	}
	port := 51820
	if err := configureWireGuard("wg0", "10.200.0.1/30", privateKey, peerKey, nil, mustPrefix("10.200.0.2/32"), &port); err != nil {
		return err
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("enable exit forwarding: %w", err)
	}
	if err := installExitRules(); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func configureWireGuard(name, address string, privateKey, peerKey wgtypes.Key, endpoint *net.UDPAddr, allowed netip.Prefix, listenPort *int) error {
	if existing, err := netlink.LinkByName(name); err == nil {
		if existing.Attrs().Alias != ownedAlias {
			return fmt.Errorf("interface %s is foreign state", name)
		}
		if err := netlink.LinkDel(existing); err != nil {
			return err
		}
	}
	attributes := netlink.NewLinkAttrs()
	attributes.Name = name
	attributes.Alias = ownedAlias
	link := &netlink.GenericLink{LinkAttrs: attributes, LinkType: "wireguard"}
	if err := netlink.LinkAdd(link); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	prefix := mustPrefix(address)
	if err := netlink.AddrReplace(link, &netlink.Addr{IPNet: prefixToIPNet(prefix)}); err != nil {
		return fmt.Errorf("address %s: %w", name, err)
	}
	client, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer client.Close()
	peer := wgtypes.PeerConfig{PublicKey: peerKey, Endpoint: endpoint, AllowedIPs: []net.IPNet{*prefixToIPNet(allowed)}}
	if endpoint != nil {
		keepalive := 5 * time.Second
		peer.PersistentKeepaliveInterval = &keepalive
	}
	if err := client.ConfigureDevice(name, wgtypes.Config{PrivateKey: &privateKey, ListenPort: listenPort, ReplacePeers: true, Peers: []wgtypes.PeerConfig{peer}}); err != nil {
		return fmt.Errorf("configure %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("activate %s: %w", name, err)
	}
	return nil
}

func installGatewayRoute(source netip.Prefix) error {
	link, err := netlink.LinkByName("tun0")
	if err != nil {
		return err
	}
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Table: gatewayRouteTab, Protocol: 99}); err != nil {
		return fmt.Errorf("install fixture tunnel default: %w", err)
	}
	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V4
	rule.Priority = 10000
	rule.Table = gatewayRouteTab
	rule.Src = prefixToIPNet(source)
	if err := netlink.RuleAdd(rule); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("install fixture tunnel source rule: %w", err)
	}
	return nil
}

func installExitRules() error {
	connection := &nftables.Conn{}
	tables, err := connection.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if table.Name == exitTableName {
			connection.DelTable(table)
		}
	}
	table := connection.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: exitTableName})
	policy := nftables.ChainPolicyDrop
	forward := connection.AddChain(&nftables.Chain{Table: table, Name: "forward", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &policy})
	postrouting := connection.AddChain(&nftables.Chain{Table: table, Name: "postrouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
	connection.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: append(interfacePair("wg0", "eth0"), &expr.Verdict{Kind: expr.VerdictAccept})})
	connection.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: append(interfacePair("eth0", "wg0"), established()...)})
	connection.AddRule(&nftables.Rule{Table: table, Chain: postrouting, Exprs: append(interfacePair("wg0", "eth0"), &expr.Masq{})})
	if err := connection.Flush(); err != nil {
		return fmt.Errorf("install fixture exit policy: %w", err)
	}
	return nil
}

func serveObservation(ctx context.Context) error {
	health := &http.Server{Addr: "127.0.0.1:9999", Handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) }), ReadHeaderTimeout: 2 * time.Second}
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/v1/vpn/status", jsonResponse(`{"status":"running"}`))
	controlMux.HandleFunc("/v1/dns/status", jsonResponse(`{"status":"running"}`))
	controlMux.HandleFunc("/v1/publicip/ip", jsonResponse(`{"public_ip":"203.0.113.10"}`))
	control := &http.Server{Addr: "127.0.0.1:8000", Handler: controlMux, ReadHeaderTimeout: 2 * time.Second}
	errorsChannel := make(chan error, 2)
	go func() { errorsChannel <- health.ListenAndServe() }()
	go func() { errorsChannel <- control.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err := <-errorsChannel:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = health.Shutdown(shutdownCtx)
	_ = control.Shutdown(shutdownCtx)
	return nil
}

func requiredEnvironment(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		panic(name + " is required")
	}
	return value
}

func mustPrefix(value string) netip.Prefix {
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		panic(err)
	}
	return prefix.Masked()
}

func prefixToIPNet(prefix netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: net.IP(prefix.Addr().AsSlice()), Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen())}
}

func interfacePair(input, output string) []expr.Any {
	return []expr.Any{&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceName(input)}, &expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceName(output)}}
}

func established() []expr.Any {
	mask := make([]byte, 4)
	binary.NativeEndian.PutUint32(mask, 0x06)
	return []expr.Any{&expr.Ct{Register: 1, Key: expr.CtKeySTATE}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: make([]byte, 4)}, &expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)}, &expr.Verdict{Kind: expr.VerdictAccept}}
}

func interfaceName(value string) []byte {
	result := make([]byte, 16)
	copy(result, value)
	return result
}

func jsonResponse(body string) http.HandlerFunc {
	return func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(body))
	}
}
