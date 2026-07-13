// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package provider

import (
	"context"
	"net/netip"
)

type EngineObservation struct {
	TunnelReady bool
	DNSReady    bool
	PublicIP    netip.Addr
}

type VPNEngine interface {
	Observe(context.Context) (EngineObservation, error)
}
