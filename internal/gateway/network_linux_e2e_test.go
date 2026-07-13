// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux && e2e

package gateway

import (
	"context"
	"os"
	"testing"
)

func TestConfigureGatewayVXLAN(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_GATEWAY_NETWORK") != "1" {
		t.Skip("runs only in the gateway network namespace")
	}
	desired := DesiredState{
		GatewayName: "e2e-private", OverlayCIDR: "172.30.99.0/24", GatewayAddress: "172.30.99.1",
		VNI: 7999, MTU: 1320, VXLANPort: 4789,
		Members: []Member{{ID: "e2e-member", OverlayAddress: "172.30.99.2", UnderlayIP: os.Getenv("WAYCLOAK_E2E_REMOTE_IP")}},
	}
	network := NewNetwork()
	if err := network.Reconcile(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if err := network.Reconcile(context.Background(), desired); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
}
