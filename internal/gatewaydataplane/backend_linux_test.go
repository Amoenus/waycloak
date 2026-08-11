// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package gatewaydataplane

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestRequireIPv4Forwarding(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "enabled", value: "1\n"},
		{name: "disabled", value: "0\n", wantErr: "forwarding is disabled"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "ip_forward")
			if err := os.WriteFile(path, []byte(test.value), 0o600); err != nil {
				t.Fatal(err)
			}
			err := requireIPv4Forwarding(path)
			if test.wantErr == "" && err != nil {
				t.Fatalf("require forwarding: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("require forwarding error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestRequireIPv4ForwardingMissing(t *testing.T) {
	t.Parallel()
	err := requireIPv4Forwarding(filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "observe namespaced IPv4 forwarding") {
		t.Fatalf("require forwarding error = %v", err)
	}
}

func TestIsIPv4DefaultRoute(t *testing.T) {
	tests := []struct {
		name  string
		route netlink.Route
		want  bool
	}{
		{name: "kernel nil destination", route: netlink.Route{}, want: true},
		{name: "explicit IPv4 zero prefix", route: netlink.Route{Dst: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}}, want: true},
		{name: "IPv4 half default", route: netlink.Route{Dst: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(1, 32)}}, want: false},
		{name: "IPv6 default", route: netlink.Route{Dst: &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isIPv4DefaultRoute(test.route); got != test.want {
				t.Fatalf("isIPv4DefaultRoute() = %t, want %t", got, test.want)
			}
		})
	}
}
