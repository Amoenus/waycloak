// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package gateway

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestSelectDefaultUnderlayRoute(t *testing.T) {
	defaultRoute := func(link, priority, table int) netlink.Route {
		return netlink.Route{LinkIndex: link, Priority: priority, Table: table}
	}
	nonDefault := defaultRoute(1, 0, 254)
	nonDefault.Dst = &net.IPNet{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)}
	explicitDefault := defaultRoute(6, 30, 254)
	explicitDefault.Dst = &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	route, err := selectDefaultUnderlayRoute([]netlink.Route{
		nonDefault,
		explicitDefault,
		defaultRoute(0, 0, 254),
		defaultRoute(9, 0, 100),
		defaultRoute(4, 100, 254),
		defaultRoute(5, 20, 0),
		defaultRoute(3, 10, 254),
		defaultRoute(2, 10, 254),
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.LinkIndex != 2 {
		t.Fatalf("selected link %d, want lowest-priority deterministic link 2", route.LinkIndex)
	}
}

func TestSelectDefaultUnderlayRouteRejectsUnusableRoutes(t *testing.T) {
	if _, err := selectDefaultUnderlayRoute([]netlink.Route{{LinkIndex: 0, Table: 254}, {LinkIndex: 1, Table: 100}}); err == nil {
		t.Fatal("expected default route without an interface to be rejected")
	}
}
