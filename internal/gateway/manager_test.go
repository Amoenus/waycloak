// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"reflect"
	"strings"
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

type observedGenerationForwarding struct {
	last DesiredState
}

func (forwarding *observedGenerationForwarding) InstallLockdown(context.Context, DesiredState) error {
	return nil
}

func (forwarding *observedGenerationForwarding) Reconcile(_ context.Context, desired DesiredState) error {
	forwarding.last = desired
	return nil
}

func (forwarding *observedGenerationForwarding) ObservePortForwardRules(_ context.Context, desired DesiredState) ([]PortForwardRuleObservation, error) {
	rules := make([]PortForwardRuleObservation, 0, len(desired.PortForwardLeases))
	for i := range desired.PortForwardLeases {
		lease := desired.PortForwardLeases[i]
		rules = append(rules, PortForwardRuleObservation{Identity: lease.Identity, LeaseGeneration: lease.LeaseGeneration, TargetAddress: lease.TargetAddress, TargetPort: lease.TargetPort, Ready: lease.LeaseGeneration > 0})
	}
	return rules, nil
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
	engine.observation = provider.EngineObservation{TunnelReady: true, DNSReady: true}
	manager.Reconcile(context.Background())
	if !manager.Ready() {
		t.Fatal("manager required optional public-IP metadata for readiness")
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

func TestHealthManagerLogsOnlyStructuredHealthTransitions(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	engine := &fakeEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true}}
	manager := &HealthManager{Engine: engine, Logger: logger}

	manager.Reconcile(context.Background())
	engine.err = errors.New("connection reset")
	engine.observation = provider.EngineObservation{}
	manager.Reconcile(context.Background())
	manager.Reconcile(context.Background())
	engine.err = nil
	engine.observation = provider.EngineObservation{TunnelReady: true, DNSReady: true}
	manager.Reconcile(context.Background())

	logs := output.String()
	if got := strings.Count(logs, `"event":"gateway_health_transition"`); got != 3 {
		t.Fatalf("health transition count=%d logs=%s", got, logs)
	}
	for _, field := range []string{`"ready":false`, `"engine_error":"connection reset"`, `"tunnel_ready":false`, `"dns_ready":false`} {
		if !strings.Contains(logs, field) {
			t.Fatalf("missing field %s in logs=%s", field, logs)
		}
	}
}

func TestHealthManagerLogsFailureReasonChangesWhileUnready(t *testing.T) {
	var output bytes.Buffer
	manager := &HealthManager{Engine: &fakeEngine{err: errors.New("connection reset")}, Source: fakeSource{err: errors.New("projection unavailable")}, Logger: slog.New(slog.NewJSONHandler(&output, nil))}

	manager.Reconcile(context.Background())
	manager.Source = fakeSource{}
	manager.Reconcile(context.Background())
	logs := output.String()
	if strings.Count(logs, `"event":"gateway_health_transition"`) != 2 || !strings.Contains(logs, `"reason":"config-error"`) || !strings.Contains(logs, `"reason":"engine-error"`) {
		t.Fatalf("failure reason transitions were not logged: %s", logs)
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
	portForwarding := &PortForwardManager{Driver: driver, Now: func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }}
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
	portForwarding := &PortForwardManager{Driver: driver, Now: func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }}
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
	if len(observations) != 1 || observations[0].GatewayRulesReady {
		t.Fatalf("stale rule generation reported ready = %#v", observations)
	}
	manager.Forwarding = fakeForwarding{rules: []PortForwardRuleObservation{{Identity: "lease", LeaseGeneration: 5, TargetAddress: "172.30.99.10", TargetPort: 8080, Ready: true}}}
	manager.Reconcile(context.Background())
	observations = manager.PortForwardingSnapshot()
	if len(observations) != 1 || !observations[0].Ready || !observations[0].GatewayRulesReady || observations[0].LeaseGeneration != 5 || observations[0].GatewayRulesGeneration != 5 || observations[0].TargetAddress != intent.TargetAddress || observations[0].TargetPort != intent.TargetPort {
		t.Fatalf("combined observation = %#v", observations)
	}
	engine.observation.TunnelReady = false
	manager.Reconcile(context.Background())
	observations = manager.PortForwardingSnapshot()
	if len(observations) != 1 || observations[0].Ready || observations[0].GatewayRulesReady {
		t.Fatalf("tunnel-loss observation = %#v", observations)
	}
}

func TestHealthManagerAppliesRotatedMappingRulesWithoutDesiredConfigRefresh(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}, TargetAddress: "172.30.99.10", TargetPort: 8080}
	forwarding := &observedGenerationForwarding{}
	portForwarding := &PortForwardManager{Driver: &fakePortForwardDriver{ports: []uint16{42000, 42001}}, Now: func() time.Time { return now }}
	manager := &HealthManager{
		Engine:         &fakeEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true, PublicIP: netip.MustParseAddr("203.0.113.10")}},
		Source:         staticSource{desired: DesiredState{PortForwardLeases: []PortForwardLeaseIntent{intent}}},
		Forwarding:     forwarding,
		PortForwarding: portForwarding,
	}

	manager.Reconcile(context.Background())
	initial := manager.PortForwardingSnapshot()[0]
	if !initial.GatewayRulesReady || initial.LeaseGeneration != 1 || initial.GatewayRulesGeneration != initial.LeaseGeneration || forwarding.last.PortForwardLeases[0].LeaseGeneration != initial.LeaseGeneration {
		t.Fatalf("initial local mapping/rule convergence = observation=%#v desired=%#v", initial, forwarding.last.PortForwardLeases)
	}

	now = now.Add(45 * time.Second)
	manager.Reconcile(context.Background())
	rotated := manager.PortForwardingSnapshot()[0]
	if !rotated.GatewayRulesReady || rotated.LeaseGeneration != initial.LeaseGeneration+1 || rotated.GatewayRulesGeneration != rotated.LeaseGeneration || forwarding.last.PortForwardLeases[0].LeaseGeneration != rotated.LeaseGeneration {
		t.Fatalf("rotated mapping did not converge locally without a source update: initial=%#v rotated=%#v desired=%#v", initial, rotated, forwarding.last.PortForwardLeases)
	}
}

func TestHealthManagerKeepsRulesDuringRecoverableRenewalFailureAndRemovesThemAtExpiry(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	driver := &fakePortForwardDriver{ports: []uint16{42000}}
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}, TargetAddress: "172.30.99.10", TargetPort: 8080}
	forwarding := &observedGenerationForwarding{}
	portForwarding := &PortForwardManager{Driver: driver, Now: func() time.Time { return now }}
	manager := &HealthManager{
		Engine:         &fakeEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true, PublicIP: netip.MustParseAddr("203.0.113.10")}},
		Source:         staticSource{desired: DesiredState{PortForwardLeases: []PortForwardLeaseIntent{intent}}},
		Forwarding:     forwarding,
		PortForwarding: portForwarding,
	}

	manager.Reconcile(context.Background())
	initial := manager.PortForwardingSnapshot()[0]
	driver.ensureErr = errors.New("temporary renewal timeout")
	now = initial.RenewAfter
	manager.Reconcile(context.Background())
	duringRetry := manager.PortForwardingSnapshot()[0]
	if !manager.Ready() || !duringRetry.Ready || !duringRetry.GatewayRulesReady || duringRetry.LeaseGeneration != initial.LeaseGeneration {
		t.Fatalf("recoverable renewal failure disrupted current mapping: initial=%#v retry=%#v error=%v", initial, duringRetry, manager.PortForwardingError())
	}

	now = initial.ExpiresAt
	manager.Reconcile(context.Background())
	expired := manager.PortForwardingSnapshot()[0]
	if manager.Ready() || expired.Ready || expired.GatewayRulesReady || forwarding.last.PortForwardLeases[0].LeaseGeneration != 0 {
		t.Fatalf("expired mapping did not fail closed: observation=%#v desired=%#v error=%v", expired, forwarding.last.PortForwardLeases, manager.PortForwardingError())
	}
}

type sequenceSource struct {
	states []DesiredState
	errs   []error
	index  int
}

func (s *sequenceSource) Load() (DesiredState, error) {
	if s.index >= len(s.states) {
		return DesiredState{}, errors.New("unexpected Load() call")
	}
	state, err := s.states[s.index], s.errs[s.index]
	s.index++
	return state, err
}

func TestHealthManagerRetainsLastConfigOnNotExist(t *testing.T) {
	var output bytes.Buffer
	forwarding := &observedGenerationForwarding{}
	source := &sequenceSource{
		states: []DesiredState{
			{MembershipGeneration: "gen-1"},
			{}, // Next call hits ErrNotExist, state should be ignored.
			{MembershipGeneration: "gen-1"},
		},
		errs: []error{
			nil,
			os.ErrNotExist,
			nil,
		},
	}
	manager := &HealthManager{
		Engine:     &fakeEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true}},
		Source:     source,
		Forwarding: forwarding,
		Logger:     slog.New(slog.NewJSONHandler(&output, nil)),
	}

	// First Reconcile: Loads normally, applies lockdown, reconcile.
	manager.Reconcile(context.Background())
	if !manager.Ready() {
		t.Fatalf("expected manager to be ready, got %v", manager.Error())
	}
	if forwarding.last.MembershipGeneration != "gen-1" {
		t.Fatalf("expected forwarding to have MembershipGeneration gen-1, got %q", forwarding.last.MembershipGeneration)
	}

	// Second Reconcile: Source returns os.ErrNotExist. Should reuse lastDesired.
	manager.Reconcile(context.Background())
	if !manager.Ready() {
		t.Fatalf("expected manager to remain ready despite os.ErrNotExist, got %v", manager.Error())
	}
	if forwarding.last.MembershipGeneration != "gen-1" {
		t.Fatalf("expected forwarding to retain MembershipGeneration gen-1, got %q", forwarding.last.MembershipGeneration)
	}

	manager.Reconcile(context.Background())
	logs := output.String()
	if strings.Count(logs, `"event":"gateway_config_source_transition"`) != 3 || !strings.Contains(logs, `"using_retained":true`) || !strings.Contains(logs, `"membership_generation":"gen-1"`) {
		t.Fatalf("config source transitions were not structured and complete: %s", logs)
	}
}
