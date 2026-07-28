// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package dataplane

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func TestDialCurrentNamespaceUsesBoundedConnectedSocket(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			_ = connection.Close()
		}
		accepted <- err
	}()
	endpoint := netip.MustParseAddrPort(listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := dialCurrentNamespace(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}

	unreachable, cancelUnreachable := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelUnreachable()
	if connection, err := dialCurrentNamespace(unreachable, netip.MustParseAddrPort("192.0.2.1:9")); err == nil {
		_ = connection.Close()
		t.Fatal("unreachable endpoint ignored the bounded context")
	}
}

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
