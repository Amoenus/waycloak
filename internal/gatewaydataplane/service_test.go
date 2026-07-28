// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/Amoenus/waycloak/internal/provider"
)

func TestReconcileDoesNotWithdrawHealthyRulesBetweenObservations(t *testing.T) {
	backend := &recordingBackend{}
	engine := &recordingEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true}}
	service := &Service{Config: testConfig(), Backend: backend, Engine: engine}
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(backend.health) != 1 || !backend.health[0] {
		t.Fatalf("healthy reconciliation transiently withdrew rules: %#v", backend.health)
	}
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(backend.health) != 2 || !backend.health[1] {
		t.Fatalf("second healthy reconciliation withdrew rules: %#v", backend.health)
	}
}
func TestReconcileWithdrawsRulesBeforeReportingEngineFailure(t *testing.T) {
	backend := &recordingBackend{}
	engine := &recordingEngine{err: errors.New("tunnel down")}
	service := &Service{Config: testConfig(), Backend: backend, Engine: engine}
	if err := service.Reconcile(context.Background()); err == nil {
		t.Fatal("engine failure was hidden")
	}
	if len(backend.health) != 1 || backend.health[0] {
		t.Fatalf("engine failure did not install deny state: %#v", backend.health)
	}
}
func TestReconcileReportsUnreadyObservation(t *testing.T) {
	backend := &recordingBackend{}
	engine := &recordingEngine{observation: provider.EngineObservation{TunnelReady: false, DNSReady: true}}
	service := &Service{Config: testConfig(), Backend: backend, Engine: engine}
	if err := service.Reconcile(context.Background()); err == nil {
		t.Fatal("unready observation was hidden")
	}
	if len(backend.health) != 1 || backend.health[0] {
		t.Fatalf("unready observation did not install deny state: %#v", backend.health)
	}
}

type recordingBackend struct{ health []bool }

func (*recordingBackend) EnsureOverlay(context.Context, Config) error { return nil }
func (backend *recordingBackend) ReplaceRules(_ context.Context, _ Config, healthy bool) error {
	backend.health = append(backend.health, healthy)
	return nil
}

type recordingEngine struct {
	observation provider.EngineObservation
	err         error
}

func (engine *recordingEngine) Observe(context.Context) (provider.EngineObservation, error) {
	return engine.observation, engine.err
}
func testConfig() Config {
	return Config{GatewayUID: "uid", OverlayCIDR: netip.MustParsePrefix("100.96.0.0/24"), GatewayAddress: netip.MustParseAddr("100.96.0.1"), OverlayInterface: "waycloak0", UnderlayInterface: "eth0", TunnelInterface: "tun0", VXLANPort: 4789, VNI: 7999, MTU: 1320, HealthPort: 18080, DNSUpstream: netip.MustParseAddrPort("127.0.0.1:53")}
}
