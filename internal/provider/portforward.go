// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package provider

import (
	"context"
	"net/netip"
	"time"
)

type PortForwardProtocol string

const (
	ProtocolTCP PortForwardProtocol = "TCP"
	ProtocolUDP PortForwardProtocol = "UDP"
)

// PortForwardCapabilities is observed provider behavior, not desired gateway
// registration. A zero MaxLeases means no lease capacity was observed.
type PortForwardCapabilities struct {
	Protocols             []PortForwardProtocol
	MaxLeases             int32
	SharedPort            bool
	SupportsRequestedPort bool
	MinimumLeaseDuration  time.Duration
}

// PortForwardLeaseRequest carries a stable Kubernetes-object identity. Drivers
// must treat repeated EnsureLease calls for the same identity as idempotent.
type PortForwardLeaseRequest struct {
	Identity              string
	InternalPort          uint16
	SuggestedExternalPort uint16
	Protocols             []PortForwardProtocol
}

type PortForwardLeaseObservation struct {
	PublicAddress netip.Addr
	PublicPort    uint16
	IssuedAt      time.Time
	RenewAfter    time.Time
	ExpiresAt     time.Time
}

// PortForwardDriver owns provider-specific acquisition and renewal only.
// Gateway DNAT and application delivery remain separate observed components.
type PortForwardDriver interface {
	ObserveCapabilities(context.Context) (PortForwardCapabilities, error)
	EnsureLease(context.Context, PortForwardLeaseRequest) (PortForwardLeaseObservation, error)
	ReleaseLease(context.Context, PortForwardLeaseRequest) error
}
