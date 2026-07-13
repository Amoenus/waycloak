// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

func TestPortForwardManagerAcquiresRenewsAndReleasesByIdentity(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	driver := &fakePortForwardDriver{ports: []uint16{42000, 42001}}
	manager := &PortForwardManager{Driver: driver, Now: func() time.Time { return now }}
	intent := PortForwardLeaseIntent{Identity: "lease-b", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}}
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
	if len(driver.ensureRequests) != 2 || driver.ensureRequests[1].SuggestedExternalPort != 42000 || manager.Snapshot()[0].PublicPort != 42001 {
		t.Fatalf("renewal requests=%#v snapshot=%#v", driver.ensureRequests, manager.Snapshot())
	}
	if err := manager.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(driver.releaseRequests) != 1 || driver.releaseRequests[0].Identity != intent.Identity || len(manager.Snapshot()) != 0 {
		t.Fatalf("release requests=%#v snapshot=%#v", driver.releaseRequests, manager.Snapshot())
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
	if err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{intent}); err == nil {
		t.Fatal("renewal failure was not returned")
	}
	observation := manager.Snapshot()[0]
	if !observation.Ready || observation.PublicPort != 42000 || observation.ExpiresAt != now.Add(15*time.Second) {
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

func TestPortForwardManagerRejectsDuplicateInternalPorts(t *testing.T) {
	manager := &PortForwardManager{Driver: &fakePortForwardDriver{}}
	err := manager.Reconcile(context.Background(), []PortForwardLeaseIntent{{Identity: "a", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}}, {Identity: "b", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolUDP}}})
	if err == nil {
		t.Fatal("duplicate internal port was accepted")
	}
}

type fakePortForwardDriver struct {
	capabilityCalls int
	ensureRequests  []provider.PortForwardLeaseRequest
	releaseRequests []provider.PortForwardLeaseRequest
	ports           []uint16
	ensureErr       error
	releaseErr      error
}

func (driver *fakePortForwardDriver) ObserveCapabilities(context.Context) (provider.PortForwardCapabilities, error) {
	driver.capabilityCalls++
	return provider.PortForwardCapabilities{Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}, SharedPort: true}, nil
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
	return provider.PortForwardLeaseObservation{PublicPort: port, IssuedAt: issued, RenewAfter: issued.Add(45 * time.Second), ExpiresAt: issued.Add(60 * time.Second)}, nil
}

func (driver *fakePortForwardDriver) ReleaseLease(_ context.Context, request provider.PortForwardLeaseRequest) error {
	driver.releaseRequests = append(driver.releaseRequests, request)
	return driver.releaseErr
}
