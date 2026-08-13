// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"testing"

	"github.com/Amoenus/waycloak/internal/provider/proton"
)

func TestPortForwardCapabilityForConfig(t *testing.T) {
	name, err := PortForwardCapabilityForConfig(map[string]string{
		"VPN_SERVICE_PROVIDER": " protonvpn ",
		"VPN_TYPE":             "OPENVPN",
	})
	if err != nil || name != PortForwardCapabilityProtonNATPMP {
		t.Fatalf("select Proton capability: name=%q err=%v", name, err)
	}
	for _, config := range []map[string]string{
		{"VPN_SERVICE_PROVIDER": "private internet access", "VPN_TYPE": "openvpn"},
		{"VPN_SERVICE_PROVIDER": "protonvpn", "VPN_TYPE": "wireguard"},
		{"VPN_SERVICE_PROVIDER": "protonvpn"},
	} {
		if name, err := PortForwardCapabilityForConfig(config); err == nil || name != "" {
			t.Fatalf("unsupported native config selected capability %q: %v", name, err)
		}
	}
}

func TestNewPortForwardCapability(t *testing.T) {
	capability, err := NewPortForwardCapability(PortForwardOptions{
		CapabilityName:  PortForwardCapabilityProtonNATPMP,
		TunnelInterface: "tunwaycloak",
	})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := capability.(*proton.Client)
	if !ok || client.TunnelInterface != "tunwaycloak" {
		t.Fatalf("unexpected capability %#v", capability)
	}
	if _, err := NewPortForwardCapability(PortForwardOptions{CapabilityName: "gluetun.waycloak.io/unknown", TunnelInterface: "tunwaycloak"}); err == nil {
		t.Fatal("unsupported capability was accepted")
	}
	if _, err := NewPortForwardCapability(PortForwardOptions{CapabilityName: PortForwardCapabilityProtonNATPMP}); err == nil {
		t.Fatal("missing tunnel interface was accepted")
	}
}
