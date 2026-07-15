// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux && e2e

package gateway

import (
	"context"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func TestConfigureGatewayVXLAN(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_GATEWAY_NETWORK") != "1" {
		t.Skip("runs only in the gateway network namespace")
	}
	desired := e2eGatewayDesired(t)
	network := NewNetwork()
	if err := network.Reconcile(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if err := network.Reconcile(context.Background(), desired); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
}

func TestConfigureGatewayVXLANWithoutMembers(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_GATEWAY_NETWORK") != "1" {
		t.Skip("runs only in the gateway network namespace")
	}
	desired := e2eGatewayDesired(t)
	desired.Members = nil
	network := NewNetwork()
	if err := network.Reconcile(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if err := network.Reconcile(context.Background(), desired); err != nil {
		t.Fatalf("idempotent empty gateway reconcile: %v", err)
	}
}

func TestConfigureGatewayForwarding(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_GATEWAY_NETWORK") != "1" {
		t.Skip("runs only in the gateway network namespace")
	}
	desired := e2eGatewayDesired(t)
	forwarding := NewForwarding()
	if err := forwarding.InstallLockdown(context.Background(), desired); err != nil {
		t.Fatalf("install gateway forwarding lockdown: %v", err)
	}
	if err := forwarding.Reconcile(context.Background(), desired); err != nil {
		t.Fatalf("configure gateway forwarding: %v", err)
	}
	if err := forwarding.Reconcile(context.Background(), desired); err != nil {
		t.Fatalf("idempotent forwarding reconcile: %v", err)
	}
}

func TestServeGatewayDNS(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_GATEWAY_DNS") != "1" {
		t.Skip("runs only in the gateway network namespace")
	}
	desired := e2eGatewayDesired(t)
	cluster := netip.MustParseAddr(os.Getenv("WAYCLOAK_E2E_CLUSTER_DNS"))
	proxy := &DNSProxy{
		ClusterUpstream:  netip.AddrPortFrom(cluster, DNSPort),
		ExternalUpstream: netip.AddrPortFrom(cluster, DNSPort),
		ClusterZones:     []string{"default.svc.cluster.local.", "svc.cluster.local.", "cluster.local."},
		Port:             GatewayDNSPort,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := proxy.Reconcile(ctx, desired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/gateway-dns-ready", []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	<-time.After(2 * time.Minute)
}

func e2eGatewayDesired(t *testing.T) DesiredState {
	t.Helper()
	remote := netip.MustParseAddr(os.Getenv("WAYCLOAK_E2E_REMOTE_IP"))
	routes, err := netlink.RouteGet(net.IP(remote.AsSlice()))
	if err != nil || len(routes) == 0 {
		t.Fatalf("resolve e2e tunnel route: %v", err)
	}
	tunnel, err := netlink.LinkByIndex(routes[0].LinkIndex)
	if err != nil {
		t.Fatalf("resolve e2e tunnel interface: %v", err)
	}
	desired := DesiredState{
		GatewayName: "e2e-private", OverlayCIDR: "172.30.99.0/24", GatewayAddress: "172.30.99.1",
		VNI: 7999, MTU: 1320, VXLANPort: 4789, TunnelInterface: tunnel.Attrs().Name,
		Members: []Member{{ID: "e2e-member", OverlayAddress: "172.30.99.2", UnderlayIP: os.Getenv("WAYCLOAK_E2E_REMOTE_IP")}},
	}
	desired.MembershipGeneration = MembershipGeneration(desired.Members)
	return desired
}
