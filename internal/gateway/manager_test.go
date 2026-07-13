// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/Amoenus/waycloak/internal/provider"
)

type fakeEngine struct {
	observation provider.EngineObservation
	err         error
}

type fakeSource struct{ err error }

func (source fakeSource) Load() (DesiredState, error) { return DesiredState{}, source.err }

type fakeNetwork struct{ err error }

func (network fakeNetwork) Reconcile(context.Context, DesiredState) error { return network.err }

type fakeForwarding struct{ err error }

func (forwarding fakeForwarding) InstallLockdown(context.Context, DesiredState) error {
	return forwarding.err
}

func (forwarding fakeForwarding) Reconcile(context.Context, DesiredState) error {
	return forwarding.err
}

type fakeDNS struct{ err error }

func (dns fakeDNS) Reconcile(context.Context, DesiredState) error { return dns.err }

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
	engine.observation.DNSReady = true
	manager.Source = fakeSource{err: errors.New("invalid desired state")}
	manager.Reconcile(context.Background())
	if manager.Ready() || manager.Error() == nil {
		t.Fatal("manager ignored invalid gateway desired state")
	}
	manager.Source = fakeSource{}
	manager.Network = fakeNetwork{err: errors.New("overlay down")}
	manager.Reconcile(context.Background())
	if manager.Ready() || manager.Error() == nil {
		t.Fatal("manager ignored gateway overlay failure")
	}
	manager.Network = fakeNetwork{}
	manager.Forwarding = fakeForwarding{err: errors.New("forwarding down")}
	manager.Reconcile(context.Background())
	if manager.Ready() || manager.Error() == nil {
		t.Fatal("manager ignored gateway forwarding failure")
	}
	manager.Forwarding = fakeForwarding{}
	manager.DNS = fakeDNS{err: errors.New("DNS listener down")}
	manager.Reconcile(context.Background())
	if manager.Ready() || manager.Error() == nil {
		t.Fatal("manager ignored gateway DNS failure")
	}
}

type orderedForwarding struct{ steps *[]string }

func (forwarding orderedForwarding) InstallLockdown(context.Context, DesiredState) error {
	*forwarding.steps = append(*forwarding.steps, "lockdown")
	return nil
}

func (forwarding orderedForwarding) Reconcile(context.Context, DesiredState) error {
	*forwarding.steps = append(*forwarding.steps, "forwarding")
	return nil
}

type orderedNetwork struct{ steps *[]string }

func (network orderedNetwork) Reconcile(context.Context, DesiredState) error {
	*network.steps = append(*network.steps, "network")
	return nil
}

type orderedDNS struct{ steps *[]string }

func (dns orderedDNS) Reconcile(context.Context, DesiredState) error {
	*dns.steps = append(*dns.steps, "dns")
	return nil
}

func TestHealthManagerInstallsGatewayLockdownBeforeOverlay(t *testing.T) {
	steps := []string{}
	manager := &HealthManager{
		Engine:     &fakeEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true, PublicIP: netip.MustParseAddr("203.0.113.10")}},
		Source:     fakeSource{},
		Forwarding: orderedForwarding{steps: &steps},
		Network:    orderedNetwork{steps: &steps},
		DNS:        orderedDNS{steps: &steps},
	}
	manager.Reconcile(context.Background())
	want := []string{"lockdown", "network", "forwarding", "dns"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("gateway reconciliation order = %#v, want %#v", steps, want)
	}
}
