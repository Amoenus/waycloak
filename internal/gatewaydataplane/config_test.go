// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"net/netip"
	"testing"
)

func TestConfigRejectsUnsafeGatewayInputs(t *testing.T) {
	valid := Config{GatewayUID: "uid", OverlayCIDR: netip.MustParsePrefix("100.96.0.0/24"), GatewayAddress: netip.MustParseAddr("100.96.0.1"), OverlayInterface: "waycloak0", UnderlayInterface: "eth0", TunnelInterface: "tun0", VXLANPort: 4789, VNI: 7999, MTU: 1320, HealthPort: 18080, DNSUpstream: netip.MustParseAddrPort("127.0.0.1:53")}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	unsafe := valid
	unsafe.DNSUpstream = netip.MustParseAddrPort("8.8.8.8:53")
	if err := unsafe.Validate(); err == nil {
		t.Fatal("non-loopback DNS upstream accepted")
	}
	unsafe = valid
	unsafe.GatewayAddress = netip.MustParseAddr("192.0.2.1")
	if err := unsafe.Validate(); err == nil {
		t.Fatal("out-of-pool gateway address accepted")
	}
}
