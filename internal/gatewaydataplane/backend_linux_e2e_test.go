// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux && e2e

package gatewaydataplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	pluginsns "github.com/containernetworking/plugins/pkg/ns"
	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestGatewayBaselineFailClosedTCPUDPAndTunnelLoss(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_GATEWAY_NETNS") != "1" {
		t.Skip("set WAYCLOAK_E2E_GATEWAY_NETNS=1 in an authorized privileged environment")
	}
	app, gateway, vpn, direct := newGatewayNS(t), newGatewayNS(t), newGatewayNS(t), newGatewayNS(t)
	setupGatewayVeth(t, app, gateway, "app0", "waycloak0")
	setupGatewayVeth(t, vpn, gateway, "vpn0", "tun0")
	setupGatewayVeth(t, direct, gateway, "wan0", "eth0")
	configureGatewayInterface(t, app, "app0", "100.96.0.2/24", "100.96.0.1")
	configureGatewayInterface(t, gateway, "waycloak0", "100.96.0.1/24", "")
	configureGatewayInterface(t, vpn, "vpn0", "10.10.0.2/24", "10.10.0.1")
	configureGatewayInterface(t, gateway, "tun0", "10.10.0.1/24", "")
	configureGatewayInterface(t, direct, "wan0", "192.0.2.2/24", "192.0.2.1")
	configureGatewayInterface(t, gateway, "eth0", "192.0.2.1/24", "192.0.2.2")
	if err := gateway.Do(func(pluginsns.NetNS) error {
		return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	vpnTCP := listenGatewayTCP(t, vpn, "10.10.0.2:18081")
	defer vpnTCP.Close()
	serveGatewayTCP(vpnTCP, "vpn")
	vpnUDP := listenGatewayUDP(t, vpn, "10.10.0.2:18082")
	defer vpnUDP.Close()
	serveGatewayUDP(vpnUDP, "vpn")
	directTCP := listenGatewayTCP(t, direct, "192.0.2.2:18081")
	defer directTCP.Close()
	serveGatewayTCP(directTCP, "direct")
	// The connected ordinary path is intentionally reachable before Waycloak
	// installs its owned chain, proving that later failures are real denial and
	// not an inert topology.
	assertGatewayTCP(t, app, "192.0.2.2:18081", "direct")
	healthTCP := listenGatewayTCP(t, gateway, "100.96.0.1:18080")
	defer healthTCP.Close()
	serveGatewayTCP(healthTCP, "health")
	installGatewayEngineFilter(t, gateway)
	config := Config{GatewayUID: "gateway-uid", OverlayCIDR: netip.MustParsePrefix("100.96.0.0/24"), GatewayAddress: netip.MustParseAddr("100.96.0.1"), OverlayInterface: "waycloak0", UnderlayInterface: "eth0", TunnelInterface: "tun0", VXLANPort: 4789, VNI: 7999, MTU: 1320, HealthPort: 18080, DNSUpstream: netip.MustParseAddrPort("127.0.0.1:53"), ClusterDNSUpstream: netip.MustParseAddrPort("10.43.0.10:53"), ClusterDomain: "cluster.local"}
	backend := LinuxBackend{}
	if err := gateway.Do(func(pluginsns.NetNS) error { return backend.ReplaceRules(context.Background(), config, false) }); err != nil {
		t.Fatal(err)
	}
	assertGatewayUnavailable(t, app, "192.0.2.2:18081")
	assertGatewayUnavailable(t, app, "100.96.0.1:18080")
	if err := gateway.Do(func(pluginsns.NetNS) error { return backend.ReplaceRules(context.Background(), config, true) }); err != nil {
		t.Fatal(err)
	}
	assertGatewayTCP(t, app, "100.96.0.1:18080", "health")
	assertGatewayTCP(t, app, "10.10.0.2:18081", "vpn")
	assertGatewayUDP(t, app, "10.10.0.2:18082", "vpn")
	assertGatewayUnavailable(t, app, "192.0.2.2:18081")
	before := ownedGatewayRuleHandles(t, gateway)
	if err := gateway.Do(func(pluginsns.NetNS) error { return backend.ReplaceRules(context.Background(), config, true) }); err != nil {
		t.Fatal(err)
	}
	if after := ownedGatewayRuleHandles(t, gateway); !reflect.DeepEqual(after, before) {
		t.Fatalf("no-op reconcile rewrote gateway rules: before=%v after=%v", before, after)
	}
	if err := gateway.Do(func(pluginsns.NetNS) error {
		link, err := netlink.LinkByName("tun0")
		if err != nil {
			return err
		}
		return netlink.LinkSetDown(link)
	}); err != nil {
		t.Fatal(err)
	}
	assertGatewayUnavailable(t, app, "192.0.2.2:18081")
	if err := gateway.Do(func(pluginsns.NetNS) error { return backend.ReplaceRules(context.Background(), config, false) }); err != nil {
		t.Fatal(err)
	}
	assertGatewayUnavailable(t, app, "192.0.2.2:18081")
	assertGatewayUnavailable(t, app, "100.96.0.1:18080")
}

func TestGatewayEnsureOverlayIsOwnedAndIdempotent(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_GATEWAY_NETNS") != "1" {
		t.Skip("set WAYCLOAK_E2E_GATEWAY_NETNS=1 in an authorized privileged environment")
	}
	gateway := newGatewayNS(t)
	config := Config{GatewayUID: "gateway-uid", OverlayCIDR: netip.MustParsePrefix("100.96.0.0/24"), GatewayAddress: netip.MustParseAddr("100.96.0.1"), OverlayInterface: "waycloak0", UnderlayInterface: "eth0", TunnelInterface: "tun0", VXLANPort: 4789, VNI: 7999, MTU: 1320, HealthPort: 18080, DNSUpstream: netip.MustParseAddrPort("127.0.0.1:53"), ClusterDNSUpstream: netip.MustParseAddrPort("10.43.0.10:53"), ClusterDomain: "cluster.local"}
	if err := gateway.Do(func(pluginsns.NetNS) error {
		if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "eth0"}}); err != nil {
			return err
		}
		link, err := netlink.LinkByName("eth0")
		if err != nil {
			return err
		}
		address, err := netlink.ParseAddr("192.0.2.1/24")
		if err != nil {
			return err
		}
		if err = netlink.AddrAdd(link, address); err != nil {
			return err
		}
		if err = netlink.LinkSetUp(link); err != nil {
			return err
		}
		if err = netlink.RouteAdd(&netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       netipPrefix(netip.IPv4Unspecified(), 0),
			Gw:        net.ParseIP("192.0.2.254"),
		}); err != nil {
			return err
		}
		if err = os.WriteFile(ipv4ForwardingPath, []byte("1\n"), 0o644); err != nil {
			return err
		}
		backend := LinuxBackend{}
		if err = backend.EnsureOverlay(context.Background(), config); err != nil {
			return err
		}
		if err = backend.EnsureOverlay(context.Background(), config); err != nil {
			return fmt.Errorf("second ensure: %w", err)
		}
		overlay, err := netlink.LinkByName(config.OverlayInterface)
		if err != nil {
			return err
		}
		if overlay.Attrs().Alias != "waycloak:"+config.GatewayUID {
			return fmt.Errorf("overlay alias = %q", overlay.Attrs().Alias)
		}
		rules, err := netlink.RuleList(netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		returnRule := false
		for _, rule := range rules {
			if rule.Priority == gatewayOverlayReturnPriority && rule.Protocol == uint8(gatewayRouteProtocol) && rule.Table == unix.RT_TABLE_MAIN && rule.Dst != nil && rule.Dst.String() == config.OverlayCIDR.Masked().String() {
				returnRule = true
				break
			}
		}
		if !returnRule {
			return errors.New("owned overlay return-path policy rule is missing")
		}
		if gatewayOverlayReturnPriority >= 99 {
			return fmt.Errorf("overlay return priority %d does not precede Gluetun priority 99", gatewayOverlayReturnPriority)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func installGatewayEngineFilter(t *testing.T, networkNS pluginsns.NetNS) {
	t.Helper()
	if err := networkNS.Do(func(pluginsns.NetNS) error {
		connection := &nftables.Conn{}
		table := connection.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: engineFilterTableName})
		policy := nftables.ChainPolicyDrop
		connection.AddChain(&nftables.Chain{Table: table, Name: engineInputChainName, Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityFilter, Policy: &policy})
		connection.AddChain(&nftables.Chain{Table: table, Name: engineForwardChainName, Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &policy})
		output := connection.AddChain(&nftables.Chain{Table: table, Name: engineOutputChainName, Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityFilter, Policy: &policy})
		connection.AddRule(&nftables.Rule{Table: table, Chain: output, Exprs: established()})
		return connection.Flush()
	}); err != nil {
		t.Fatal(err)
	}
}

func ownedGatewayRuleHandles(t *testing.T, networkNS pluginsns.NetNS) []string {
	t.Helper()
	result := []string{}
	if err := networkNS.Do(func(pluginsns.NetNS) error {
		connection := &nftables.Conn{}
		chains, err := connection.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
		if err != nil {
			return err
		}
		for _, chain := range chains {
			if chain.Table == nil || (chain.Table.Name != coreTableName && chain.Table.Name != engineFilterTableName) {
				continue
			}
			rules, err := connection.GetRules(chain.Table, chain)
			if err != nil {
				return err
			}
			for _, rule := range rules {
				if strings.HasPrefix(string(rule.UserData), "waycloak:") {
					result = append(result, fmt.Sprintf("%s/%s/%d", chain.Table.Name, chain.Name, rule.Handle))
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(result)
	return result
}

func newGatewayNS(t *testing.T) pluginsns.NetNS {
	t.Helper()
	name := fmt.Sprintf("waycloak-gw-%d-%d", os.Getpid(), time.Now().UnixNano())
	var createErr error
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		runtime.LockOSThread()
		original, err := vnetns.Get()
		if err != nil {
			createErr = err
			return
		}
		defer original.Close()
		created, err := vnetns.NewNamed(name)
		if err != nil {
			createErr = err
			return
		}
		created.Close()
		createErr = vnetns.Set(original)
	}()
	group.Wait()
	if createErr != nil {
		t.Fatal(createErr)
	}
	networkNS, err := pluginsns.GetNS(filepath.Join("/var/run/netns", name))
	if err != nil {
		_ = vnetns.DeleteNamed(name)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = networkNS.Close(); _ = vnetns.DeleteNamed(name) })
	return networkNS
}
func setupGatewayVeth(t *testing.T, endpoint, gateway pluginsns.NetNS, endpointName, gatewayName string) {
	t.Helper()
	if err := endpoint.Do(func(pluginsns.NetNS) error {
		return netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: endpointName, MTU: 1500}, PeerName: gatewayName, PeerNamespace: netlink.NsFd(int(gateway.Fd()))})
	}); err != nil {
		t.Fatal(err)
	}
}
func configureGatewayInterface(t *testing.T, networkNS pluginsns.NetNS, name, cidr, gateway string) {
	t.Helper()
	if err := networkNS.Do(func(pluginsns.NetNS) error {
		loopback, err := netlink.LinkByName("lo")
		if err != nil {
			return err
		}
		if err = netlink.LinkSetUp(loopback); err != nil {
			return err
		}
		link, err := netlink.LinkByName(name)
		if err != nil {
			return err
		}
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			return err
		}
		if err = netlink.AddrAdd(link, address); err != nil {
			return err
		}
		if err = netlink.LinkSetUp(link); err != nil {
			return err
		}
		if gateway != "" {
			return netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(gateway)})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func listenGatewayTCP(t *testing.T, networkNS pluginsns.NetNS, address string) net.Listener {
	t.Helper()
	var listener net.Listener
	if err := networkNS.Do(func(pluginsns.NetNS) error { var err error; listener, err = net.Listen("tcp4", address); return err }); err != nil {
		t.Fatal(err)
	}
	return listener
}
func listenGatewayUDP(t *testing.T, networkNS pluginsns.NetNS, address string) *net.UDPConn {
	t.Helper()
	var listener *net.UDPConn
	if err := networkNS.Do(func(pluginsns.NetNS) error {
		peer, err := net.ResolveUDPAddr("udp4", address)
		if err != nil {
			return err
		}
		listener, err = net.ListenUDP("udp4", peer)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return listener
}
func serveGatewayTCP(listener net.Listener, identity string) {
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				request := make([]byte, 32)
				count, err := connection.Read(request)
				if err == nil {
					_, _ = connection.Write([]byte(identity + ":" + string(request[:count])))
				}
			}()
		}
	}()
}
func serveGatewayUDP(listener *net.UDPConn, identity string) {
	go func() {
		buffer := make([]byte, 256)
		for {
			count, peer, err := listener.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = listener.WriteToUDP([]byte(identity+":"+string(buffer[:count])), peer)
		}
	}()
}
func assertGatewayTCP(t *testing.T, networkNS pluginsns.NetNS, address, identity string) {
	t.Helper()
	if err := networkNS.Do(func(pluginsns.NetNS) error {
		connection, err := net.DialTimeout("tcp4", address, 2*time.Second)
		if err != nil {
			return err
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err = connection.Write([]byte("probe")); err != nil {
			return err
		}
		response := make([]byte, len(identity)+6)
		if _, err = io.ReadFull(connection, response); err != nil {
			return err
		}
		if string(response) != identity+":probe" {
			return errors.New("response used the wrong path")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func assertGatewayUDP(t *testing.T, networkNS pluginsns.NetNS, address, identity string) {
	t.Helper()
	if err := networkNS.Do(func(pluginsns.NetNS) error {
		peer, err := net.ResolveUDPAddr("udp4", address)
		if err != nil {
			return err
		}
		connection, err := net.DialUDP("udp4", nil, peer)
		if err != nil {
			return err
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err = connection.Write([]byte("probe")); err != nil {
			return err
		}
		response := make([]byte, 64)
		count, err := connection.Read(response)
		if err != nil {
			return err
		}
		if string(response[:count]) != identity+":probe" {
			return errors.New("UDP response used the wrong path")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func assertGatewayUnavailable(t *testing.T, networkNS pluginsns.NetNS, address string) {
	t.Helper()
	if err := networkNS.Do(func(pluginsns.NetNS) error {
		connection, err := net.DialTimeout("tcp4", address, 400*time.Millisecond)
		if err == nil {
			connection.Close()
			return errors.New("ordinary path remained reachable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
