// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/Amoenus/waycloak/internal/provider"
)

type fakeEngine struct {
	observation provider.EngineObservation
	err         error
}

func (engine *fakeEngine) Observe(context.Context) (provider.EngineObservation, error) {
	return engine.observation, engine.err
}

func TestHealthManagerTracksObservedEngineState(t *testing.T) {
	engine := &fakeEngine{err: errors.New("tunnel down")}
	manager := &HealthManager{Engine: engine}
	manager.Reconcile(context.Background())
	if manager.Ready() {
		t.Fatal("manager reported desired registration as ready")
	}
	engine.err = nil
	engine.observation = provider.EngineObservation{TunnelReady: true, DNSReady: true, PublicIP: netip.MustParseAddr("203.0.113.10")}
	manager.Reconcile(context.Background())
	if !manager.Ready() {
		t.Fatal("manager did not report a complete observation as ready")
	}
	engine.observation.DNSReady = false
	manager.Reconcile(context.Background())
	if manager.Ready() {
		t.Fatal("manager remained ready after DNS observation failed")
	}
}
