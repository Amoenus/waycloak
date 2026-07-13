// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import "testing"

func TestDesiredStateValidationPreservesStableMemberIdentities(t *testing.T) {
	desired := DesiredState{GatewayName: "private", OverlayCIDR: "172.30.99.0/24", GatewayAddress: "172.30.99.1", VNI: 7999, MTU: 1320, VXLANPort: 4789, Members: []Member{{ID: "workload-a", OverlayAddress: "172.30.99.2", UnderlayIP: "10.42.0.2"}, {ID: "workload-b", OverlayAddress: "172.30.99.3", UnderlayIP: "10.42.1.2"}}}
	if err := desired.Validate(); err != nil {
		t.Fatal(err)
	}
	desired.Members[1].OverlayAddress = desired.Members[0].OverlayAddress
	if err := desired.Validate(); err == nil {
		t.Fatal("duplicate stable allocation was accepted")
	}
}
