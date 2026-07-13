// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"testing"

	"github.com/Amoenus/waycloak/internal/provider"
)

func TestDesiredStateValidationPreservesStableMemberIdentities(t *testing.T) {
	desired := DesiredState{GatewayName: "private", OverlayCIDR: "172.30.99.0/24", GatewayAddress: "172.30.99.1", VNI: 7999, MTU: 1320, VXLANPort: 4789, TunnelInterface: TunnelInterface, Members: []Member{{ID: "workload-a", OverlayAddress: "172.30.99.2", UnderlayIP: "10.42.0.2"}, {ID: "workload-b", OverlayAddress: "172.30.99.3", UnderlayIP: "10.42.1.2"}}}
	if err := desired.Validate(); err != nil {
		t.Fatal(err)
	}
	desired.Members[1].OverlayAddress = desired.Members[0].OverlayAddress
	if err := desired.Validate(); err == nil {
		t.Fatal("duplicate stable allocation was accepted")
	}
}

func TestDesiredStateRejectsDuplicatePortForwardMappingIdentity(t *testing.T) {
	desired := DesiredState{GatewayName: "private", OverlayCIDR: "172.30.99.0/24", GatewayAddress: "172.30.99.1", VNI: 7999, MTU: 1320, VXLANPort: 4789, TunnelInterface: TunnelInterface, Members: []Member{{ID: "a", OverlayAddress: "172.30.99.10", UnderlayIP: "192.0.2.10"}, {ID: "b", OverlayAddress: "172.30.99.11", UnderlayIP: "192.0.2.11"}}, PortForwardLeases: []PortForwardLeaseIntent{{Identity: "a", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}, TargetAddress: "172.30.99.10", TargetPort: 80}, {Identity: "b", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolUDP}, TargetAddress: "172.30.99.11", TargetPort: 80}}}
	if err := desired.Validate(); err == nil {
		t.Fatal("duplicate provider internal port was accepted")
	}
}

func TestDesiredStateRejectsPortForwardTargetOutsideObservedMembership(t *testing.T) {
	desired := DesiredState{GatewayName: "private", OverlayCIDR: "172.30.99.0/24", GatewayAddress: "172.30.99.1", VNI: 7999, MTU: 1320, VXLANPort: 4789, TunnelInterface: TunnelInterface, Members: []Member{{ID: "member", OverlayAddress: "172.30.99.10", UnderlayIP: "192.0.2.10"}}, PortForwardLeases: []PortForwardLeaseIntent{{Identity: "lease", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}, TargetAddress: "172.30.99.11", TargetPort: 80, LeaseGeneration: 1}}}
	if err := desired.Validate(); err == nil {
		t.Fatal("port-forward target outside current membership was accepted")
	}
}
