// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e && linux

package dataplane

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
)

func TestLockdownDropsDirectPackets(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_NETNS") != "1" {
		t.Skip("runs only in the isolated e2e network namespace")
	}
	target := net.JoinHostPort(os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"))
	if err := connect(target, time.Second); err != nil {
		t.Fatalf("control connection before lockdown: %v", err)
	}

	conn := &nftables.Conn{}
	unrelatedName := fmt.Sprintf("unrelated_%d", os.Getpid())
	conn.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: unrelatedName})
	if err := conn.Flush(); err != nil {
		t.Fatalf("create unrelated nftables table: %v", err)
	}

	backend := NewBackend()
	if err := backend.Preflight(context.Background()); err != nil {
		t.Fatalf("preflight in capable network namespace: %v", err)
	}
	const uid = "00000000-0000-0000-0000-000000000001"
	if err := backend.InstallLockdown(context.Background(), uid); err != nil {
		t.Fatalf("install lockdown: %v", err)
	}
	if err := backend.InstallLockdown(context.Background(), uid); err != nil {
		t.Fatalf("idempotent lockdown: %v", err)
	}
	if err := connect(target, 500*time.Millisecond); err == nil {
		t.Fatal("direct Kubernetes Service packet escaped after lockdown")
	}

	tables, err := (&nftables.Conn{}).ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		t.Fatal(err)
	}
	foundOwned, foundUnrelated := false, false
	for _, table := range tables {
		foundOwned = foundOwned || table.Name == policyTableName(uid)
		foundUnrelated = foundUnrelated || table.Name == unrelatedName
	}
	if !foundOwned || !foundUnrelated {
		t.Fatalf("table ownership after lockdown: owned=%t unrelated=%t", foundOwned, foundUnrelated)
	}
}

func TestApplicationPortRedirectTCPRotation(t *testing.T) {
	testApplicationPortRedirectRotation(t, "TCP")
}

func TestApplicationPortRedirectUDPRotation(t *testing.T) {
	testApplicationPortRedirectRotation(t, "UDP")
}

func testApplicationPortRedirectRotation(t *testing.T, protocol string) {
	if os.Getenv("WAYCLOAK_E2E_PORT_REDIRECT") != "1" {
		t.Skip("runs only in the isolated application network namespace")
	}
	const uid = "00000000-0000-0000-0000-000000000003"
	address := netip.MustParseAddr("172.31.0.2")
	for index, applicationPort := range []uint16{42000, 42001} {
		redirect := ApplicationPortRedirect{Identity: "lease-uid", TargetPort: 6881, ApplicationPort: applicationPort, Protocols: []string{protocol}}
		var listener net.Listener
		var packetListener net.PacketConn
		var err error
		if protocol == "TCP" {
			listener, err = net.Listen("tcp4", net.JoinHostPort(address.String(), fmt.Sprint(applicationPort)))
		} else {
			packetListener, err = (&net.ListenConfig{}).ListenPacket(context.Background(), "udp4", net.JoinHostPort(address.String(), fmt.Sprint(applicationPort)))
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := replacePolicy(uid, "", netip.AddrPort{}, "target0", netip.Addr{}, address, ClusterTrafficGateway, nil, []ApplicationPortRedirect{redirect}); err != nil {
			closeRedirectListener(listener, packetListener)
			t.Fatal(err)
		}
		marker := fmt.Sprintf("/tmp/application-port-%s-ready-%d", strings.ToLower(protocol), index+1)
		if err := os.WriteFile(marker, []byte("ready\n"), 0o600); err != nil {
			closeRedirectListener(listener, packetListener)
			t.Fatal(err)
		}
		var payload []byte
		if listener != nil {
			_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
			connection, acceptErr := listener.Accept()
			if acceptErr == nil {
				payload, err = io.ReadAll(io.LimitReader(connection, 64))
				_ = connection.Close()
			} else {
				err = acceptErr
			}
		} else {
			_ = packetListener.SetDeadline(time.Now().Add(30 * time.Second))
			buffer := make([]byte, 64)
			var length int
			length, _, err = packetListener.ReadFrom(buffer)
			payload = buffer[:length]
		}
		closeRedirectListener(listener, packetListener)
		if err != nil || string(payload) != fmt.Sprintf("generation-%d", index+1) {
			t.Fatalf("redirected payload=%q error=%v", payload, err)
		}
	}
}

func closeRedirectListener(listener net.Listener, packetListener net.PacketConn) {
	if listener != nil {
		_ = listener.Close()
	}
	if packetListener != nil {
		_ = packetListener.Close()
	}
}

func connect(target string, timeout time.Duration) error {
	connection, err := net.DialTimeout("tcp", target, timeout)
	if err == nil {
		_ = connection.Close()
	}
	return err
}

func TestFakeGatewayEndpoint(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_GATEWAY") != "1" {
		t.Skip("runs only in the fake gateway network namespace")
	}
	if os.Getenv("WAYCLOAK_E2E_SKIP_GATEWAY_VXLAN") != "1" {
		local := netip.MustParseAddr(os.Getenv("WAYCLOAK_E2E_LOCAL_IP"))
		remote := netip.MustParseAddr(os.Getenv("WAYCLOAK_E2E_REMOTE_IP"))
		routes, err := netlink.RouteGet(net.IP(remote.AsSlice()))
		if err != nil || len(routes) == 0 {
			t.Fatalf("resolve fake gateway underlay: %v", err)
		}
		route := routes[0]
		link := &netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: "wc-fake-gw", MTU: 1320}, VxlanId: 7999, VtepDevIndex: route.LinkIndex, SrcAddr: net.IP(local.AsSlice()), Group: net.IP(remote.AsSlice()), Port: 4789, Learning: false, NoAge: true}
		if err := netlink.LinkAdd(link); err != nil {
			t.Fatalf("create fake gateway VXLAN: %v", err)
		}
		if err := netlink.AddrReplace(link, &netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("172.30.99.1"), Mask: net.CIDRMask(24, 32)}}); err != nil {
			t.Fatalf("address fake gateway VXLAN: %v", err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatalf("bring fake gateway VXLAN up: %v", err)
		}
	}
	if os.Getenv("WAYCLOAK_E2E_SKIP_GATEWAY_DNS") != "1" {
		stopDNS := startFakeDNSProxy(t, net.JoinHostPort(os.Getenv("WAYCLOAK_E2E_CLUSTER_DNS"), "53"))
		defer stopDNS()
	}
	listener, err := net.Listen("tcp4", "172.30.99.1:18080")
	if err != nil {
		t.Fatalf("listen on fake protected endpoint: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) })
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	if err := os.WriteFile("/tmp/gateway-ready", []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve protected health endpoint: %v", err)
		}
	case <-time.After(2 * time.Minute):
	}
}

func TestConfigureVXLANProtectedPath(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_CLIENT") != "1" {
		t.Skip("runs only in the protected client network namespace")
	}
	cfg := e2eClientConfig()
	agent := Agent{Backend: NewBackend()}
	if err := agent.Prepare(context.Background(), cfg); err != nil {
		t.Fatalf("prepare protected path: %v", err)
	}
	if err := agent.Verify(context.Background(), cfg); err != nil {
		t.Fatalf("verify protected path: %v", err)
	}
	if err := connect("172.30.99.1:18080", 3*time.Second); err != nil {
		t.Fatalf("reach fake gateway over VXLAN: %v", err)
	}
	assertDNSReachability(t, true)
	assertGatewayClusterPath(t, true)
}

func TestProtectedStateSurvivesAgentExit(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_CLIENT") != "1" {
		t.Skip("runs only in the protected client network namespace")
	}
	wantGateway := os.Getenv("WAYCLOAK_E2E_EXPECT_GATEWAY") == "1"
	err := connect("172.30.99.1:18080", time.Second)
	if wantGateway && err != nil {
		t.Fatalf("protected path disappeared after agent exit: %v", err)
	}
	if !wantGateway {
		deadline := time.Now().Add(15 * time.Second)
		for err == nil && time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			err = connect("172.30.99.1:18080", 500*time.Millisecond)
		}
		if err == nil {
			t.Fatal("protected path remained reachable after fake gateway loss")
		}
	}
	assertDNSReachability(t, wantGateway)
	assertForwardedTarget(t, wantGateway)
}

func startFakeDNSProxy(t *testing.T, upstream string) func() {
	t.Helper()
	udp, err := net.ListenPacket("udp4", fmt.Sprintf("172.30.99.1:%d", contract.GatewayDNSPort))
	if err != nil {
		t.Fatalf("listen on fake gateway UDP DNS: %v", err)
	}
	tcp, err := net.Listen("tcp4", fmt.Sprintf("172.30.99.1:%d", contract.GatewayDNSPort))
	if err != nil {
		_ = udp.Close()
		t.Fatalf("listen on fake gateway TCP DNS: %v", err)
	}
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, client, readErr := udp.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			request := append([]byte(nil), buffer[:n]...)
			go func() {
				upstreamConnection, dialErr := net.DialTimeout("udp", upstream, time.Second)
				if dialErr != nil {
					return
				}
				defer upstreamConnection.Close()
				_ = upstreamConnection.SetDeadline(time.Now().Add(2 * time.Second))
				if _, writeErr := upstreamConnection.Write(request); writeErr != nil {
					return
				}
				response := make([]byte, 65535)
				responseLength, responseErr := upstreamConnection.Read(response)
				if responseErr == nil {
					_, _ = udp.WriteTo(response[:responseLength], client)
				}
			}()
		}
	}()
	go func() {
		for {
			client, acceptErr := tcp.Accept()
			if acceptErr != nil {
				return
			}
			go proxyTCPDNS(client, upstream)
		}
	}()
	return func() {
		_ = udp.Close()
		_ = tcp.Close()
	}
}

func proxyTCPDNS(client net.Conn, upstream string) {
	defer client.Close()
	server, err := net.DialTimeout("tcp", upstream, time.Second)
	if err != nil {
		return
	}
	defer server.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
	<-done
}

func assertDNSReachability(t *testing.T, want bool) {
	t.Helper()
	for _, network := range []string{"udp", "tcp"} {
		err := exchangeDNS(network, "192.0.2.53:53")
		if want && err != nil {
			t.Fatalf("gateway-routed %s DNS failed: %v", network, err)
		}
		if !want && err == nil {
			t.Fatalf("%s DNS bypassed the unavailable gateway", network)
		}
	}
	if want {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := net.DefaultResolver.LookupHost(ctx, "kubernetes.default"); err != nil {
			t.Fatalf("Kubernetes search-domain lookup through gateway DNS: %v", err)
		}
	}
}

func exchangeDNS(network, target string) error {
	query := dnsQuery("kubernetes.default.svc.cluster.local")
	connection, err := net.DialTimeout(network, target, time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if network == "tcp" {
		length := make([]byte, 2)
		binary.BigEndian.PutUint16(length, uint16(len(query)))
		if _, err = connection.Write(append(length, query...)); err != nil {
			return err
		}
		if _, err = io.ReadFull(connection, length); err != nil {
			return err
		}
		response := make([]byte, binary.BigEndian.Uint16(length))
		_, err = io.ReadFull(connection, response)
		return validateDNSResponse(response, query[:2])
	}
	if _, err = connection.Write(query); err != nil {
		return err
	}
	response := make([]byte, 65535)
	n, err := connection.Read(response)
	if err != nil {
		return err
	}
	return validateDNSResponse(response[:n], query[:2])
}

func dnsQuery(name string) []byte {
	query := make([]byte, 12)
	binary.BigEndian.PutUint16(query[0:2], 0x5743)
	binary.BigEndian.PutUint16(query[2:4], 0x0100)
	binary.BigEndian.PutUint16(query[4:6], 1)
	for _, label := range strings.Split(name, ".") {
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 1, 0, 1)
	return query
}

func validateDNSResponse(response, id []byte) error {
	if len(response) < 12 || !bytes.Equal(response[:2], id) {
		return errors.New("invalid DNS response")
	}
	if response[3]&0x0f != 0 {
		return fmt.Errorf("DNS response code %d", response[3]&0x0f)
	}
	return nil
}

func TestRepairOwnedFirewallAndLinkDrift(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_CLIENT") != "1" {
		t.Skip("runs only in the protected client network namespace")
	}
	cfg := e2eClientConfig()
	conn := &nftables.Conn{}
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if table.Name == policyTableName(cfg.PodUID) {
			conn.DelTable(table)
		}
	}
	if err := conn.Flush(); err != nil {
		t.Fatalf("remove owned policy for drift test: %v", err)
	}
	rules, err := netlink.RuleList(familyFor(cfg.GatewayEndpoint.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	for i := range rules {
		if rules[i].Protocol == waycloakRuleProtocol {
			if err := netlink.RuleDel(&rules[i]); err != nil {
				t.Fatalf("remove owned policy rule for drift test: %v", err)
			}
		}
	}
	routes, err := netlink.RouteListFiltered(familyFor(cfg.GatewayAddress), &netlink.Route{Table: protectedRouteTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	for i := range routes {
		if err := netlink.RouteDel(&routes[i]); err != nil {
			t.Fatalf("remove owned route for drift test: %v", err)
		}
	}
	link, err := netlink.LinkByName(overlayName(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}
	agent := Agent{Backend: NewBackend()}
	if err := agent.Repair(context.Background(), cfg); err != nil {
		t.Fatalf("repair owned drift: %v", err)
	}
	if err := connect("172.30.99.1:18080", 2*time.Second); err != nil {
		t.Fatalf("protected path after drift repair: %v", err)
	}
	assertDNSReachability(t, true)
	assertGatewayClusterPath(t, true)
}

func TestClusterTrafficModes(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_CLIENT") != "1" {
		t.Skip("runs only in the protected client network namespace")
	}
	serviceIP := netip.MustParseAddr(os.Getenv("KUBERNETES_SERVICE_HOST"))
	cfg := e2eClientConfig()
	cfg.ClusterCIDRs = []netip.Prefix{netip.PrefixFrom(serviceIP, serviceIP.BitLen())}
	agent := Agent{Backend: NewBackend()}

	cfg.ClusterTrafficMode = ClusterTrafficPreserve
	if err := agent.Repair(context.Background(), cfg); err != nil {
		t.Fatalf("apply Preserve mode: %v", err)
	}
	target := net.JoinHostPort(serviceIP.String(), os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"))
	if err := connect(target, 2*time.Second); err != nil {
		t.Fatalf("Preserve mode did not retain declared cluster destination: %v", err)
	}
	assertDNSReachability(t, true)

	cfg.ClusterTrafficMode = ClusterTrafficDeny
	if err := agent.Repair(context.Background(), cfg); err != nil {
		t.Fatalf("apply Deny mode: %v", err)
	}
	if err := connect(target, 500*time.Millisecond); err == nil {
		t.Fatal("Deny mode allowed a declared cluster destination")
	}
	assertDNSReachability(t, true)

	cfg.ClusterTrafficMode = ClusterTrafficGateway
	if err := agent.Repair(context.Background(), cfg); err != nil {
		t.Fatalf("restore Gateway mode: %v", err)
	}
	assertGatewayClusterPath(t, true)
	if err := connect("172.30.99.1:18080", 2*time.Second); err != nil {
		t.Fatalf("Gateway mode lost protected connectivity: %v", err)
	}
	assertDNSReachability(t, true)
}

func e2eClientConfig() Config {
	remote := netip.MustParseAddr(os.Getenv("WAYCLOAK_E2E_REMOTE_IP"))
	return Config{
		PodUID:             "00000000-0000-0000-0000-000000000002",
		Address:            netip.MustParsePrefix("172.30.99.2/24"),
		OverlayCIDR:        netip.MustParsePrefix("172.30.99.0/24"),
		GatewayAddress:     netip.MustParseAddr("172.30.99.1"),
		GatewayEndpoint:    netip.AddrPortFrom(remote, 4789),
		GatewayHealthPort:  18080,
		VNI:                7999,
		MTU:                1320,
		ClusterTrafficMode: ClusterTrafficGateway,
	}
}

func assertGatewayClusterPath(t *testing.T, gatewayAvailable bool) {
	t.Helper()
	target := net.JoinHostPort(os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"))
	err := connect(target, 2*time.Second)
	wantReachable := gatewayAvailable && os.Getenv("WAYCLOAK_E2E_GATEWAY_FORWARDING") == "1"
	if wantReachable && err != nil {
		t.Fatalf("Gateway mode did not route the Kubernetes Service through the gateway: %v", err)
	}
	if !wantReachable && err == nil {
		t.Fatal("Kubernetes Service remained reachable without the protected gateway path")
	}
}

func assertForwardedTarget(t *testing.T, want bool) {
	t.Helper()
	target := os.Getenv("WAYCLOAK_E2E_FORWARD_TARGET")
	if target == "" {
		return
	}
	err := connect(target, 2*time.Second)
	if want && err != nil {
		t.Fatalf("gateway-forwarded target is unreachable: %v", err)
	}
	if !want {
		deadline := time.Now().Add(15 * time.Second)
		for err == nil && time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			err = connect(target, 500*time.Millisecond)
		}
		if err == nil {
			t.Fatal("gateway-forwarded target remained reachable after gateway teardown")
		}
	}
}
