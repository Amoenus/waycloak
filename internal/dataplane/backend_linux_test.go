// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package dataplane

import (
	"net"
	"net/netip"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestVXLANMatchesObservedGatewayEndpoint(t *testing.T) {
	underlay := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Index: 7, Name: "eth0"}}
	source := net.ParseIP("10.42.1.8")
	cfg := Config{
		GatewayEndpoint: netip.MustParseAddrPort("10.42.18.60:4789"),
		VNI:             7999,
		MTU:             1320,
	}
	vxlan := &netlink.Vxlan{
		LinkAttrs:    netlink.LinkAttrs{MTU: 1320},
		VxlanId:      7999,
		VtepDevIndex: 7,
		SrcAddr:      source,
		Group:        net.ParseIP("10.42.18.60"),
		Port:         4789,
	}
	if !vxlanMatches(vxlan, cfg, underlay, source) {
		t.Fatal("current gateway endpoint was treated as stale")
	}

	stale := *vxlan
	stale.Group = net.ParseIP("10.42.18.51")
	if vxlanMatches(&stale, cfg, underlay, source) {
		t.Fatal("stale gateway endpoint was accepted")
	}
}
