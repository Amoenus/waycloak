// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

type fakeEngine struct {
	observation provider.EngineObservation
	err         error
}

type fakeSource struct{ err error }

func (source fakeSource) Load() (DesiredState, error) { return DesiredState{}, source.err }

type staticSource struct{ desired DesiredState }

func (source staticSource) Load() (DesiredState, error) { return source.desired, nil }

type mutableSource struct {
	desired DesiredState
	err     error
}

func (source *mutableSource) Load() (DesiredState, error) { return source.desired, source.err }

type fakeNetwork struct{ err error }

func (network fakeNetwork) Reconcile(context.Context, DesiredState) error { return network.err }

type fakeForwarding struct {
	err   error
	rules []PortForwardRuleObservation
}

func (forwarding fakeForwarding) InstallLockdown(context.Context, DesiredState) error {
	return forwarding.err
}

func (forwarding fakeForwarding) Reconcile(context.Context, DesiredState) error {
	return forwarding.err
}

func (forwarding fakeForwarding) ObservePortForwardRules(context.Context, DesiredState) ([]PortForwardRuleObservation, error) {
	return forwarding.rules, forwarding.err
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

func TestHealthManagerRetainsLastAppliedMembershipGeneration(t *testing.T) {
	oldMembers := []Member{{ID: "a", OverlayAddress: "172.30.99.2", UnderlayIP: "10.42.0.2"}}
	oldGeneration := MembershipGeneration(oldMembers)
	source := &mutableSource{desired: DesiredState{MembershipGeneration: oldGeneration, Members: oldMembers}}
	manager := &HealthManager{Engine: &fakeEngine{}, Source: source}
	manager.Reconcile(context.Background())
	if got := manager.AppliedMembershipGeneration(); got != oldGeneration {
		t.Fatalf("applied generation = %q", got)
	}
	source.err = errors.New("partially projected configuration")
	manager.Reconcile(context.Background())
	if got := manager.AppliedMembershipGeneration(); got != oldGeneration {
		t.Fatalf("malformed projection replaced last-known-good generation with %q", got)
	}
	source.err = nil
	newMembers := append(oldMembers, Member{ID: "b", OverlayAddress: "172.30.99.3", UnderlayIP: "10.42.0.3"})
	newGeneration := MembershipGeneration(newMembers)
	source.desired = DesiredState{MembershipGeneration: newGeneration, Members: newMembers}
	manager.Reconcile(context.Background())
	if got := manager.AppliedMembershipGeneration(); got != newGeneration {
		t.Fatalf("advanced applied generation = %q", got)
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

func (forwarding orderedForwarding) ObservePortForwardRules(context.Context, DesiredState) ([]PortForwardRuleObservation, error) {
	*forwarding.steps = append(*forwarding.steps, "observe-port-forward-rules")
	return nil, nil
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
	want := []string{"lockdown", "network", "forwarding", "observe-port-forward-rules", "dns"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("gateway reconciliation order = %#v, want %#v", steps, want)
	}
}

func TestHealthManagerAcquiresProviderLeaseOnlyThroughObservedTunnel(t *testing.T) {
	engine := &fakeEngine{err: errors.New("tunnel down")}
	driver := &fakePortForwardDriver{ports: []uint16{42000}}
	portForwarding := &PortForwardManager{Driver: driver}
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}}
	manager := &HealthManager{Engine: engine, Source: staticSource{desired: DesiredState{PortForwardLeases: []PortForwardLeaseIntent{intent}}}, PortForwarding: portForwarding}
	manager.Reconcile(context.Background())
	if len(driver.ensureRequests) != 0 {
		t.Fatal("provider lease was attempted without an observed tunnel")
	}
	engine.err = nil
	engine.observation = provider.EngineObservation{TunnelReady: true, DNSReady: true, PublicIP: netip.MustParseAddr("203.0.113.10")}
	manager.Reconcile(context.Background())
	if len(driver.ensureRequests) != 1 || manager.PortForwardingError() != nil {
		t.Fatalf("provider requests=%#v error=%v", driver.ensureRequests, manager.PortForwardingError())
	}
}

func TestHealthManagerReadinessIncludesPortForwardReconciliation(t *testing.T) {
	driver := &fakePortForwardDriver{ensureErr: errors.New("provider unavailable")}
	portForwarding := &PortForwardManager{Driver: driver}
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}}
	manager := &HealthManager{
		Engine:         &fakeEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true, PublicIP: netip.MustParseAddr("203.0.113.10")}},
		Source:         staticSource{desired: DesiredState{PortForwardLeases: []PortForwardLeaseIntent{intent}}},
		PortForwarding: portForwarding,
	}
	manager.Reconcile(context.Background())
	if manager.Ready() || manager.PortForwardingError() == nil {
		t.Fatal("manager reported ready after provider reconciliation failed")
	}
	driver.ensureErr = nil
	manager.Reconcile(context.Background())
	if !manager.Ready() || manager.PortForwardingError() != nil {
		t.Fatalf("manager did not recover after provider reconciliation: %v", manager.PortForwardingError())
	}
}

func TestHealthManagerPublishesOnlyObservedExactGatewayRules(t *testing.T) {
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}, TargetAddress: "172.30.99.10", TargetPort: 8080, LeaseGeneration: 4}
	portForwarding := &PortForwardManager{Driver: &fakePortForwardDriver{ports: []uint16{42000}}, Now: func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }}
	engine := &fakeEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true, PublicIP: netip.MustParseAddr("203.0.113.10")}}
	manager := &HealthManager{
		Engine:         engine,
		Source:         staticSource{desired: DesiredState{PortForwardLeases: []PortForwardLeaseIntent{intent}}},
		Forwarding:     fakeForwarding{rules: []PortForwardRuleObservation{{Identity: "lease", LeaseGeneration: 4, TargetAddress: "172.30.99.10", TargetPort: 8080, Ready: true}}},
		PortForwarding: portForwarding,
	}
	manager.Reconcile(context.Background())
	observations := manager.PortForwardingSnapshot()
	if len(observations) != 1 || !observations[0].Ready || !observations[0].GatewayRulesReady || observations[0].GatewayRulesGeneration != 4 || observations[0].TargetAddress != intent.TargetAddress || observations[0].TargetPort != intent.TargetPort {
		t.Fatalf("combined observation = %#v", observations)
	}
	engine.observation.TunnelReady = false
	manager.Reconcile(context.Background())
	observations = manager.PortForwardingSnapshot()
	if len(observations) != 1 || observations[0].Ready || observations[0].GatewayRulesReady {
		t.Fatalf("tunnel-loss observation = %#v", observations)
	}
}
