// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"context"
	"flag"
	"log"
	"net/netip"
	"os/signal"
	"syscall"
	"time"

	"github.com/Amoenus/waycloak/internal/gatewaydataplane"
	"github.com/Amoenus/waycloak/internal/provider/gluetun"
)

func main() {
	var uid, overlayCIDR, gatewayAddress, overlayInterface, underlayInterface, tunnelInterface, dnsUpstream, clusterDNSUpstream, clusterDomain, controlAPIKeyFile string
	var vxlanPort, healthPort uint
	var vni uint
	var mtu int
	var interval time.Duration
	flag.StringVar(&uid, "gateway-uid", "", "exact VPNGateway UID")
	flag.StringVar(&overlayCIDR, "overlay-cidr", "", "reviewed overlay CIDR")
	flag.StringVar(&gatewayAddress, "gateway-address", "", "gateway overlay address")
	flag.StringVar(&overlayInterface, "overlay-interface", "waycloak0", "owned overlay interface")
	flag.StringVar(&underlayInterface, "underlay-interface", "eth0", "Pod underlay interface")
	flag.StringVar(&tunnelInterface, "tunnel-interface", "tun0", "VPN tunnel interface")
	flag.StringVar(&dnsUpstream, "dns-upstream", "127.0.0.1:53", "loopback engine DNS upstream")
	flag.StringVar(&clusterDNSUpstream, "cluster-dns-upstream", "", "reviewed Kubernetes DNS Service upstream")
	flag.StringVar(&clusterDomain, "cluster-domain", "", "reviewed Kubernetes cluster DNS suffix")
	flag.StringVar(&controlAPIKeyFile, "gluetun-control-api-key-file", "", "optional Pod-local Gluetun control API identity")
	flag.UintVar(&vxlanPort, "vxlan-port", 4789, "VXLAN UDP port")
	flag.UintVar(&healthPort, "health-port", 18080, "overlay health port")
	flag.UintVar(&vni, "vni", 7999, "reviewed VXLAN network identifier")
	flag.IntVar(&mtu, "mtu", 1320, "reviewed overlay MTU")
	flag.DurationVar(&interval, "reconcile-interval", time.Second, "engine observation and fail-closed rule interval")
	flag.Parse()
	pool, err := netip.ParsePrefix(overlayCIDR)
	if err != nil {
		log.Fatal(err)
	}
	address, err := netip.ParseAddr(gatewayAddress)
	if err != nil {
		log.Fatal(err)
	}
	upstream, err := netip.ParseAddrPort(dnsUpstream)
	if err != nil {
		log.Fatal(err)
	}
	clusterUpstream, err := netip.ParseAddrPort(clusterDNSUpstream)
	if err != nil {
		log.Fatal(err)
	}
	engine := gluetun.New()
	engine.APIKeyFile = controlAPIKeyFile
	service := &gatewaydataplane.Service{Config: gatewaydataplane.Config{GatewayUID: uid, OverlayCIDR: pool.Masked(), GatewayAddress: address, OverlayInterface: overlayInterface, UnderlayInterface: underlayInterface, TunnelInterface: tunnelInterface, DNSUpstream: upstream, ClusterDNSUpstream: clusterUpstream, ClusterDomain: clusterDomain, VXLANPort: uint16(vxlanPort), HealthPort: uint16(healthPort), VNI: uint32(vni), MTU: mtu}, Backend: gatewaydataplane.LinuxBackend{}, Engine: engine, ReconcileErrorHook: func(err error) {
		log.Printf("gateway_reconcile_transition state=not_ready fail_closed=true error=%q", err)
	}, ReconcileRecoveryHook: func(previousError string, unavailableFor time.Duration) {
		log.Printf("gateway_reconcile_transition state=ready recovered=true unavailable_for=%s previous_error=%q", unavailableFor.Round(time.Millisecond), previousError)
	}}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := service.Run(ctx, interval); err != nil {
		log.Fatal(err)
	}
}
