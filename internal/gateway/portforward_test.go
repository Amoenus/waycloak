// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

func TestPortForwardManagerAcquiresRenewsAndReleasesByIdentity(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	driver := &fakePortForwardDriver{ports: []uint16{42000, 42001}}
	manager := &PortForwardManager{Driver: driver, Now: func() time.Time { return now }}
	intent := PortForwardLeaseIntent{Identity: "lease-b", InternalPort: 7, SuggestedExternalPort: 41000, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}}
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	if driver.capabilityCalls != 1 || len(driver.ensureRequests) != 1 || driver.ensureRequests[0].SuggestedExternalPort != 0 {
		t.Fatalf("initial driver calls = %#v", driver)
	}
	if got := manager.Snapshot(); len(got) != 1 || !got[0].Ready || got[0].PublicPort != 42000 {
		t.Fatalf("initial snapshot = %#v", got)
	}
	now = now.Add(30 * time.Second)
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil || len(driver.ensureRequests) != 1 {
		t.Fatalf("premature renewal: calls=%d error=%v", len(driver.ensureRequests), err)
	}
	now = now.Add(15 * time.Second)
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	if len(driver.ensureRequests) != 2 || driver.ensureRequests[1].SuggestedExternalPort != 0 || manager.Snapshot()[0].PublicPort != 42001 {
		t.Fatalf("renewal requests=%#v snapshot=%#v", driver.ensureRequests, manager.Snapshot())
	}
	if err := manager.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(driver.releaseRequests) != 1 || driver.releaseRequests[0].Identity != intent.Identity || len(manager.Snapshot()) != 0 {
		t.Fatalf("release requests=%#v snapshot=%#v", driver.releaseRequests, manager.Snapshot())
	}
}

func TestPortForwardManagerReusesPublicPortOnlyWhenDriverSupportsRequests(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	driver := &fakePortForwardDriver{ports: []uint16{42000, 42000}, supportsRequestedPort: true}
	manager := &PortForwardManager{Driver: driver, Now: func() time.Time { return now }}
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 7, SuggestedExternalPort: 41000, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}}
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	if driver.ensureRequests[0].SuggestedExternalPort != 41000 {
		t.Fatalf("initial request = %#v", driver.ensureRequests[0])
	}
	now = now.Add(45 * time.Second)
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	if len(driver.ensureRequests) != 2 || driver.ensureRequests[1].SuggestedExternalPort != 42000 {
		t.Fatalf("renewal requests = %#v", driver.ensureRequests)
	}
}

func TestPortForwardManagerOwnsMappingGenerationAcrossRenewalAndRotation(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	driver := &fakePortForwardDriver{ports: []uint16{42000, 42000, 42001}}
	manager := &PortForwardManager{Driver: driver, Now: func() time.Time { return now }}
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}, LeaseGeneration: 4}

	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	initial := manager.Snapshot()[0]
	if initial.LeaseGeneration != 5 {
		t.Fatalf("initial manager-owned generation = %d, want 5", initial.LeaseGeneration)
	}

	now = now.Add(45 * time.Second)
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	renewed := manager.Snapshot()[0]
	if renewed.LeaseGeneration != initial.LeaseGeneration || !renewed.ExpiresAt.After(initial.ExpiresAt) {
		t.Fatalf("expiry-only renewal changed mapping generation: initial=%#v renewed=%#v", initial, renewed)
	}

	now = now.Add(45 * time.Second)
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	rotated := manager.Snapshot()[0]
	if rotated.LeaseGeneration != initial.LeaseGeneration+1 || rotated.PublicPort == renewed.PublicPort {
		t.Fatalf("endpoint rotation did not advance mapping generation: renewed=%#v rotated=%#v", renewed, rotated)
	}
}

func TestPortForwardManagerRecoversFromStaleRestartSeedWithoutReusingEndpointGeneration(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	driver := &fakePortForwardDriver{ports: []uint16{42001}}
	manager := &PortForwardManager{Driver: driver, Now: func() time.Time { return now }}
	intent := PortForwardLeaseIntent{
		Identity:                 "lease",
		InternalPort:             7,
		Protocols:                []provider.PortForwardProtocol{provider.ProtocolTCP},
		SuggestedExternalAddress: "203.0.113.10",
		SuggestedExternalPort:    42000,
		LeaseGeneration:          5,
	}
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	first := manager.Snapshot()[0]
	if first.LeaseGeneration != 6 || first.PublicPort != 42001 {
		t.Fatalf("fresh acquisition from stale seed = %#v", first)
	}

	// The controller's previously current generation and endpoint eventually
	// arrive through ConfigMap projection. The manager must advance the new
	// endpoint beyond that generation without another provider request.
	intent.LeaseGeneration = 6
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	recovered := manager.Snapshot()[0]
	if recovered.LeaseGeneration != 7 || len(driver.ensureRequests) != 1 {
		t.Fatalf("stale restart seed did not converge safely: observation=%#v requests=%d", recovered, len(driver.ensureRequests))
	}
}

func TestPortForwardLeaseObservationRequiresCurrentPublicEndpoint(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	valid := provider.PortForwardLeaseObservation{PublicAddress: netip.MustParseAddr("203.0.113.10"), PublicPort: 42000, IssuedAt: now.Add(-time.Second), RenewAfter: now.Add(44 * time.Second), ExpiresAt: now.Add(59 * time.Second)}
	if !validPortForwardLeaseObservation(valid, now) {
		t.Fatal("current provider public endpoint was rejected")
	}
	valid.PublicAddress = netip.Addr{}
	if validPortForwardLeaseObservation(valid, now) {
		t.Fatal("provider observation without a public address was accepted")
	}
}

func TestPortForwardManagerKeepsUnexpiredObservationDuringRenewalFailure(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	driver := &fakePortForwardDriver{ports: []uint16{42000}}
	manager := &PortForwardManager{Driver: driver, Now: func() time.Time { return now }}
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}}
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Second)
	driver.ensureErr = errors.New("temporary network failure")
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatalf("recoverable renewal failure disrupted reconciliation: %v", err)
	}
	observation := manager.Snapshot()[0]
	if !observation.Ready || !observation.RenewalPending || observation.PublicPort != 42000 || observation.ExpiresAt != now.Add(15*time.Second) {
		t.Fatalf("unexpired observation = %#v", observation)
	}
	now = now.Add(16 * time.Second)
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err == nil {
		t.Fatal("expired renewal failure was not returned")
	}
	if manager.Snapshot()[0].Ready {
		t.Fatalf("expired observation remained ready: %#v", manager.Snapshot()[0])
	}
}

func TestPortForwardManagerSnapshotRemainsResponsiveDuringProviderIO(t *testing.T) {
	issuedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	var nowUnix atomic.Int64
	nowUnix.Store(issuedAt.Unix())
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}}
	driver := &blockingPortForwardDriver{started: make(chan struct{}, 1), release: make(chan struct{})}
	manager := &PortForwardManager{
		Driver:               driver,
		Now:                  func() time.Time { return time.Unix(nowUnix.Load(), 0).UTC() },
		capabilitiesObserved: true,
		capabilities:         provider.PortForwardCapabilities{Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}},
		leases: map[string]managedPortForwardLease{
			intent.Identity: {
				intent: intent,
				observation: PortForwardObservation{
					Identity:      intent.Identity,
					InternalPort:  intent.InternalPort,
					Protocols:     intent.Protocols,
					PublicAddress: "203.0.113.10",
					PublicPort:    42000,
					IssuedAt:      issuedAt.Add(-45 * time.Second),
					RenewAfter:    issuedAt,
					ExpiresAt:     issuedAt.Add(15 * time.Second),
					Ready:         true,
					Message:       "Provider mapping is current",
				},
			},
		},
	}

	reconciled := make(chan error, 1)
	go func() { reconciled <- manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}) }()
	select {
	case <-driver.started:
	case <-time.After(time.Second):
		close(driver.release)
		t.Fatal("provider renewal did not start")
	}

	snapshots := make(chan []PortForwardObservation, 1)
	go func() { snapshots <- manager.Snapshot() }()
	select {
	case snapshot := <-snapshots:
		if len(snapshot) != 1 || !snapshot[0].Ready || snapshot[0].PublicPort != 42000 {
			t.Fatalf("in-flight snapshot = %#v", snapshot)
		}
	case <-time.After(250 * time.Millisecond):
		close(driver.release)
		<-reconciled
		t.Fatal("snapshot blocked behind provider I/O")
	}

	nowUnix.Store(issuedAt.Add(16 * time.Second).Unix())
	if snapshot := manager.Snapshot(); len(snapshot) != 1 || snapshot[0].Ready {
		t.Fatalf("expired in-flight snapshot = %#v", snapshot)
	}
	close(driver.release)
	if err := <-reconciled; err != nil {
		t.Fatalf("recoverable blocked renewal failure disrupted reconciliation: %v", err)
	}
}

func TestPortForwardManagerRejectsDuplicateInternalPorts(t *testing.T) {
	manager := &PortForwardManager{Driver: &fakePortForwardDriver{}}
	err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{{Identity: "a", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}}, {Identity: "b", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolUDP}}})
	if err == nil {
		t.Fatal("duplicate internal port was accepted")
	}
}

func TestPortForwardManagerDoesNotRotateProviderMappingWhenOnlyTargetChanges(t *testing.T) {
	driver := &fakePortForwardDriver{ports: []uint16{42000}}
	manager := &PortForwardManager{Driver: driver, Now: func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }}
	intent := PortForwardLeaseIntent{Identity: "lease", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}, TargetAddress: "172.30.99.10", TargetPort: 80, LeaseGeneration: 1}
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	intent.TargetAddress = "172.30.99.11"
	intent.TargetPort = 8080
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	if len(driver.ensureRequests) != 1 || len(driver.releaseRequests) != 0 {
		t.Fatalf("target-only change touched provider mapping: ensures=%#v releases=%#v", driver.ensureRequests, driver.releaseRequests)
	}
}

type fakePortForwardDriver struct {
	capabilityCalls       int
	ensureRequests        []provider.PortForwardLeaseRequest
	releaseRequests       []provider.PortForwardLeaseRequest
	ports                 []uint16
	ensureErr             error
	releaseErr            error
	supportsRequestedPort bool
}

type blockingPortForwardDriver struct {
	started chan struct{}
	release chan struct{}
}

func (driver *blockingPortForwardDriver) ObserveCapabilities(context.Context) (provider.PortForwardCapabilities, error) {
	return provider.PortForwardCapabilities{}, errors.New("unexpected capability observation")
}

func (driver *blockingPortForwardDriver) EnsureLease(context.Context, provider.PortForwardLeaseRequest) (provider.PortForwardLeaseObservation, error) {
	driver.started <- struct{}{}
	<-driver.release
	return provider.PortForwardLeaseObservation{}, errors.New("renewal failed")
}

func (driver *blockingPortForwardDriver) ReleaseLease(context.Context, provider.PortForwardLeaseRequest) error {
	return errors.New("unexpected release")
}

func (driver *fakePortForwardDriver) ObserveCapabilities(context.Context) (provider.PortForwardCapabilities, error) {
	driver.capabilityCalls++
	return provider.PortForwardCapabilities{Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}, SharedPort: true, SupportsRequestedPort: driver.supportsRequestedPort}, nil
}

func (driver *fakePortForwardDriver) EnsureLease(_ context.Context, request provider.PortForwardLeaseRequest) (provider.PortForwardLeaseObservation, error) {
	driver.ensureRequests = append(driver.ensureRequests, request)
	if driver.ensureErr != nil {
		return provider.PortForwardLeaseObservation{}, driver.ensureErr
	}
	port := uint16(42000)
	if len(driver.ports) >= len(driver.ensureRequests) {
		port = driver.ports[len(driver.ensureRequests)-1]
	}
	issued := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).Add(time.Duration(len(driver.ensureRequests)-1) * 45 * time.Second)
	return provider.PortForwardLeaseObservation{PublicAddress: netip.MustParseAddr("203.0.113.10"), PublicPort: port, IssuedAt: issued, RenewAfter: issued.Add(45 * time.Second), ExpiresAt: issued.Add(60 * time.Second)}, nil
}

func (driver *fakePortForwardDriver) ReleaseLease(_ context.Context, request provider.PortForwardLeaseRequest) error {
	driver.releaseRequests = append(driver.releaseRequests, request)
	return driver.releaseErr
}
