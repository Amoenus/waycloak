// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e && linux

package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/gatewaycontract"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"
)

func TestGatewayPortForwardTCPUDPReturnSymmetryHandoffAndWithdrawal(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_PORT_FORWARD_NETNS") != "1" {
		t.Skip("runs only with explicit privileged network-namespace authorization")
	}
	gatewayNS := newPortForwardNS(t)
	vpnNS := newPortForwardNS(t)
	podANS := newPortForwardNS(t)
	podBNS := newPortForwardNS(t)

	setupVeth(t, vpnNS, gatewayNS, "eth0", "tun0")
	setupVeth(t, podANS, gatewayNS, "eth0", "poda0")
	setupVeth(t, podBNS, gatewayNS, "eth0", "podb0")
	configureInterface(t, gatewayNS, "tun0", "192.0.2.1/24", "")
	configureOverlayBridge(t, gatewayNS, "way0", "10.42.0.1/24", "poda0", "podb0")
	configureInterface(t, vpnNS, "eth0", "192.0.2.2/24", "192.0.2.1")
	configureInterface(t, podANS, "eth0", "10.42.0.10/24", "10.42.0.1")
	configureInterface(t, podBNS, "eth0", "10.42.0.11/24", "10.42.0.1")
	if err := gatewayNS.Do(func(ns.NetNS) error {
		return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644)
	}); err != nil {
		t.Fatalf("enable isolated gateway forwarding: %v", err)
	}
	installPortForwardBaselineFilters(t, gatewayNS)

	tcpA := listenTCP(t, podANS, "10.42.0.10:6881")
	defer tcpA.Close()
	udpA := listenUDP(t, podANS, "10.42.0.10:6881")
	defer udpA.Close()
	tcpB := listenTCP(t, podBNS, "10.42.0.11:6881")
	defer tcpB.Close()
	udpB := listenUDP(t, podBNS, "10.42.0.11:6881")
	defer udpB.Close()
	serveTCPIdentity(tcpA, "pod-a")
	serveUDPIdentity(udpA, "pod-a")
	serveTCPIdentity(tcpB, "pod-b")
	serveUDPIdentity(udpB, "pod-b")
	egressTCP := listenTCP(t, vpnNS, "192.0.2.2:8443")
	defer egressTCP.Close()
	serveTCPIdentity(egressTCP, "vpn-egress")

	rule := GatewayRule{LeaseUID: "lease-a", HandoffGeneration: 1, IngressPort: 50000,
		OverlayAddress: "10.42.0.10", TargetPort: 6881, Protocols: []wayv1.TransportProtocol{wayv1.ProtocolTCP, wayv1.ProtocolUDP}}
	backend := LinuxRuleBackend{TunnelInterface: "tun0", OverlayInterface: "way0"}
	if err := gatewayNS.Do(func(ns.NetNS) error { return backend.Replace(context.Background(), []GatewayRule{rule}) }); err != nil {
		t.Fatalf("program gateway rules: %v", err)
	}
	if err := gatewayNS.Do(func(ns.NetNS) error {
		ready, err := backend.Ready(context.Background(), rule)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("exact gateway rules were not observed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertTCPIdentity(t, vpnNS, "192.0.2.1:50000", "pod-a")
	assertUDPIdentityAndSource(t, vpnNS, "192.0.2.1:50000", "pod-a")
	// The port-forward catch-all must not drop established tunnel replies for
	// ordinary protected-workload egress evaluated by the other base chains.
	assertTCPIdentity(t, podANS, "192.0.2.2:8443", "vpn-egress")

	successor := rule
	successor.HandoffGeneration = 2
	successor.OverlayAddress = "10.42.0.11"
	if err := gatewayNS.Do(func(ns.NetNS) error { return backend.Replace(context.Background(), []GatewayRule{successor}) }); err != nil {
		t.Fatalf("atomically hand off gateway rules: %v", err)
	}
	if err := gatewayNS.Do(func(ns.NetNS) error {
		withdrawn, err := backend.Withdrawn(context.Background(), rule.LeaseUID, rule.HandoffGeneration)
		if err != nil {
			return err
		}
		if !withdrawn {
			return errors.New("old generation remained programmed after handoff")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertTCPIdentity(t, vpnNS, "192.0.2.1:50000", "pod-b")
	assertUDPIdentityAndSource(t, vpnNS, "192.0.2.1:50000", "pod-b")

	if err := gatewayNS.Do(func(ns.NetNS) error { return backend.Replace(context.Background(), nil) }); err != nil {
		t.Fatalf("withdraw gateway rules: %v", err)
	}
	if err := gatewayNS.Do(func(ns.NetNS) error {
		withdrawn, err := backend.Withdrawn(context.Background(), successor.LeaseUID, successor.HandoffGeneration)
		if err != nil {
			return err
		}
		if !withdrawn {
			return errors.New("withdrawn rule markers remain")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertTCPUnavailable(t, vpnNS, "192.0.2.1:50000")
	assertUDPUnavailable(t, vpnNS, "192.0.2.1:50000")
}

func installPortForwardBaselineFilters(t *testing.T, networkNS ns.NetNS) {
	t.Helper()
	if err := networkNS.Do(func(ns.NetNS) error {
		connection := &nftables.Conn{}
		policy := nftables.ChainPolicyDrop
		for _, tableName := range []string{"filter", "waycloak_gateway_core_fixture"} {
			table := connection.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: tableName})
			chain := connection.AddChain(&nftables.Chain{Table: table, Name: "forward", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &policy})
			connection.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes("way0")},
				&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes("tun0")},
				&expr.Verdict{Kind: expr.VerdictAccept},
			}})
			connection.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes("tun0")},
				&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes("way0")},
				&expr.Meta{Key: expr.MetaKeyMARK, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(gatewaycontract.PortForwardIngressMark)},
				&expr.Verdict{Kind: expr.VerdictAccept},
			}})
			connection.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes("tun0")},
				&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceNameBytes("way0")},
				&expr.Ct{Register: 1, Key: expr.CtKeySTATE}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0x06, 0, 0, 0}, Xor: []byte{0, 0, 0, 0}},
				&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}}, &expr.Verdict{Kind: expr.VerdictAccept},
			}})
		}
		return connection.Flush()
	}); err != nil {
		t.Fatalf("install fail-closed baseline filters: %v", err)
	}
}

func configureOverlayBridge(t *testing.T, networkNS ns.NetNS, name, cidr string, peers ...string) {
	t.Helper()
	if err := networkNS.Do(func(ns.NetNS) error {
		bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: name}}
		if err := netlink.LinkAdd(bridge); err != nil {
			return err
		}
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			return err
		}
		if err := netlink.AddrAdd(bridge, address); err != nil {
			return err
		}
		if err := netlink.LinkSetUp(bridge); err != nil {
			return err
		}
		for _, peerName := range peers {
			peer, err := netlink.LinkByName(peerName)
			if err != nil {
				return err
			}
			if err := netlink.LinkSetMaster(peer, bridge); err != nil {
				return err
			}
			if err := netlink.LinkSetUp(peer); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("configure overlay bridge: %v", err)
	}
}

func newPortForwardNS(t *testing.T) ns.NetNS {
	t.Helper()
	name := fmt.Sprintf("waycloak-pf-%d-%d", os.Getpid(), time.Now().UnixNano())
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
	networkNS, err := ns.GetNS(filepath.Join("/var/run/netns", name))
	if err != nil {
		_ = vnetns.DeleteNamed(name)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = networkNS.Close()
		_ = vnetns.DeleteNamed(name)
	})
	return networkNS
}

func setupVeth(t *testing.T, endpointNS, gatewayNS ns.NetNS, endpointName, gatewayName string) {
	t.Helper()
	if err := endpointNS.Do(func(ns.NetNS) error {
		return netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: endpointName, MTU: 1500}, PeerName: gatewayName, PeerNamespace: netlink.NsFd(int(gatewayNS.Fd()))})
	}); err != nil {
		t.Fatalf("create %s/%s veth: %v", endpointName, gatewayName, err)
	}
}

func configureInterface(t *testing.T, networkNS ns.NetNS, name, cidr, gateway string) {
	t.Helper()
	if err := networkNS.Do(func(ns.NetNS) error {
		loopback, err := netlink.LinkByName("lo")
		if err != nil {
			return err
		}
		if err := netlink.LinkSetUp(loopback); err != nil {
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
		if err := netlink.AddrAdd(link, address); err != nil {
			return err
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return err
		}
		if gateway != "" {
			return netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(gateway)})
		}
		return nil
	}); err != nil {
		t.Fatalf("configure %s: %v", name, err)
	}
}

func listenTCP(t *testing.T, networkNS ns.NetNS, address string) net.Listener {
	t.Helper()
	var listener net.Listener
	if err := networkNS.Do(func(ns.NetNS) error {
		var err error
		listener, err = net.Listen("tcp4", address)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return listener
}

func listenUDP(t *testing.T, networkNS ns.NetNS, address string) *net.UDPConn {
	t.Helper()
	var listener *net.UDPConn
	if err := networkNS.Do(func(ns.NetNS) error {
		udpAddress, err := net.ResolveUDPAddr("udp4", address)
		if err != nil {
			return err
		}
		listener, err = net.ListenUDP("udp4", udpAddress)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return listener
}

func serveTCPIdentity(listener net.Listener, identity string) {
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				request := make([]byte, 64)
				length, err := connection.Read(request)
				if err == nil {
					_, _ = connection.Write([]byte(identity + ":" + string(request[:length])))
				}
			}()
		}
	}()
}

func serveUDPIdentity(listener *net.UDPConn, identity string) {
	go func() {
		buffer := make([]byte, 2048)
		for {
			length, peer, err := listener.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = listener.WriteToUDP([]byte(identity+":"+string(buffer[:length])), peer)
		}
	}()
}

func assertTCPIdentity(t *testing.T, networkNS ns.NetNS, address, identity string) {
	t.Helper()
	if err := networkNS.Do(func(ns.NetNS) error {
		connection, err := net.DialTimeout("tcp4", address, 2*time.Second)
		if err != nil {
			return err
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := connection.Write([]byte("tcp-waycloak")); err != nil {
			return err
		}
		response := make([]byte, len(identity+":tcp-waycloak"))
		if _, err := io.ReadFull(connection, response); err != nil {
			return err
		}
		if string(response) != identity+":tcp-waycloak" {
			return errors.New("TCP response crossed to the wrong endpoint")
		}
		return nil
	}); err != nil {
		t.Fatalf("TCP exact-target delivery: %v", err)
	}
}

func assertUDPIdentityAndSource(t *testing.T, networkNS ns.NetNS, address, identity string) {
	t.Helper()
	if err := networkNS.Do(func(ns.NetNS) error {
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
		if _, err := connection.Write([]byte("udp-waycloak")); err != nil {
			return err
		}
		response := make([]byte, 64)
		length, source, err := connection.ReadFromUDP(response)
		if err != nil {
			return err
		}
		if string(response[:length]) != identity+":udp-waycloak" || source.IP.String() != "192.0.2.1" || source.Port != 50000 {
			return errors.New("UDP return path did not preserve the provider address and port")
		}
		return nil
	}); err != nil {
		t.Fatalf("UDP exact-target delivery and return symmetry: %v", err)
	}
}

func assertTCPUnavailable(t *testing.T, networkNS ns.NetNS, address string) {
	t.Helper()
	if err := networkNS.Do(func(ns.NetNS) error {
		connection, err := net.DialTimeout("tcp4", address, 500*time.Millisecond)
		if err == nil {
			connection.Close()
			return errors.New("withdrawn TCP port remained reachable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertUDPUnavailable(t *testing.T, networkNS ns.NetNS, address string) {
	t.Helper()
	if err := networkNS.Do(func(ns.NetNS) error {
		peer, err := net.ResolveUDPAddr("udp4", address)
		if err != nil {
			return err
		}
		connection, err := net.DialUDP("udp4", nil, peer)
		if err != nil {
			return err
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := connection.Write([]byte("withdrawn")); err != nil {
			return nil
		}
		buffer := make([]byte, 64)
		if _, err := connection.Read(buffer); err == nil {
			return errors.New("withdrawn UDP port delivered a response")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
