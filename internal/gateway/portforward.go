// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

type PortForwardObservation struct {
	Identity     string                         `json:"identity"`
	InternalPort uint16                         `json:"internalPort"`
	Protocols    []provider.PortForwardProtocol `json:"protocols"`
	PublicPort   uint16                         `json:"publicPort,omitempty"`
	IssuedAt     time.Time                      `json:"issuedAt,omitempty"`
	RenewAfter   time.Time                      `json:"renewAfter,omitempty"`
	ExpiresAt    time.Time                      `json:"expiresAt,omitempty"`
	Ready        bool                           `json:"ready"`
	Releasing    bool                           `json:"releasing,omitempty"`
	Message      string                         `json:"message,omitempty"`
}

const PortForwardObservationAPIVersion = "networking.waycloak.io/v1alpha1"

type PortForwardObservationDocument struct {
	APIVersion string                 `json:"apiVersion"`
	Lease      PortForwardObservation `json:"lease"`
}

type managedPortForwardLease struct {
	intent      PortForwardLeaseIntent
	observation PortForwardObservation
}

// PortForwardManager owns renewable provider mappings but not gateway DNAT.
// Its snapshot is safe for a read-only local control endpoint.
type PortForwardManager struct {
	Driver provider.PortForwardDriver
	Now    func() time.Time

	mu                       sync.RWMutex
	leases                   map[string]managedPortForwardLease
	capabilities             provider.PortForwardCapabilities
	capabilitiesObserved     bool
	capabilityObservationErr error
}

func (manager *PortForwardManager) Reconcile(ctx context.Context, desired []PortForwardLeaseIntent) error {
	if manager.Driver == nil {
		if len(desired) == 0 {
			return nil
		}
		return errors.New("port-forward provider driver is not configured")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.leases == nil {
		manager.leases = map[string]managedPortForwardLease{}
	}
	if !manager.capabilitiesObserved {
		manager.capabilities, manager.capabilityObservationErr = manager.Driver.ObserveCapabilities(ctx)
		manager.capabilitiesObserved = manager.capabilityObservationErr == nil
		if manager.capabilityObservationErr != nil {
			return manager.capabilityObservationErr
		}
	}

	wanted := make(map[string]PortForwardLeaseIntent, len(desired))
	internalPorts := make(map[uint16]string, len(desired))
	for _, intent := range desired {
		if intent.Identity == "" || intent.InternalPort == 0 || len(intent.Protocols) == 0 {
			return errors.New("invalid port-forward lease intent")
		}
		if _, exists := wanted[intent.Identity]; exists {
			return fmt.Errorf("duplicate port-forward lease identity %q", intent.Identity)
		}
		if identity, exists := internalPorts[intent.InternalPort]; exists {
			return fmt.Errorf("port-forward internal port %d is shared by %q and %q", intent.InternalPort, identity, intent.Identity)
		}
		wanted[intent.Identity] = intent
		internalPorts[intent.InternalPort] = intent.Identity
	}

	var reconcileErr error
	for identity, managed := range manager.leases {
		if _, exists := wanted[identity]; exists {
			continue
		}
		managed.observation.Ready = false
		managed.observation.Releasing = true
		managed.observation.Message = "Provider mapping release is pending"
		manager.leases[identity] = managed
		request := providerRequest(managed.intent, managed.observation.PublicPort)
		if err := manager.Driver.ReleaseLease(ctx, request); err != nil {
			managed.observation.Message = "Provider mapping release failed: " + err.Error()
			manager.leases[identity] = managed
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		delete(manager.leases, identity)
	}

	now := manager.now()
	for _, intent := range desired {
		managed, exists := manager.leases[intent.Identity]
		intentChanged := exists && !reflect.DeepEqual(managed.intent, intent)
		if intentChanged {
			if err := manager.Driver.ReleaseLease(ctx, providerRequest(managed.intent, managed.observation.PublicPort)); err != nil {
				managed.observation.Ready = false
				managed.observation.Message = "Previous provider mapping release failed: " + err.Error()
				manager.leases[intent.Identity] = managed
				reconcileErr = errors.Join(reconcileErr, err)
				continue
			}
			exists = false
		}
		needsEnsure := !exists || !managed.observation.Ready || !now.Before(managed.observation.RenewAfter)
		if !needsEnsure {
			continue
		}
		suggestedPort := intent.SuggestedExternalPort
		if exists && managed.observation.PublicPort != 0 {
			suggestedPort = managed.observation.PublicPort
		}
		observation, err := manager.Driver.EnsureLease(ctx, providerRequest(intent, suggestedPort))
		if err != nil {
			if !exists || !now.Before(managed.observation.ExpiresAt) {
				managed.observation = PortForwardObservation{Identity: intent.Identity, InternalPort: intent.InternalPort, Protocols: append([]provider.PortForwardProtocol(nil), intent.Protocols...)}
			}
			managed.intent = intent
			managed.observation.Message = "Provider mapping renewal failed: " + err.Error()
			manager.leases[intent.Identity] = managed
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		managed.intent = intent
		managed.observation = PortForwardObservation{Identity: intent.Identity, InternalPort: intent.InternalPort, Protocols: append([]provider.PortForwardProtocol(nil), intent.Protocols...)}
		managed.observation.PublicPort = observation.PublicPort
		managed.observation.IssuedAt = observation.IssuedAt
		managed.observation.RenewAfter = observation.RenewAfter
		managed.observation.ExpiresAt = observation.ExpiresAt
		managed.observation.Ready = true
		managed.observation.Message = "Provider mapping is current"
		manager.leases[intent.Identity] = managed
	}
	return reconcileErr
}

func (manager *PortForwardManager) Snapshot() []PortForwardObservation {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	observations := make([]PortForwardObservation, 0, len(manager.leases))
	for _, managed := range manager.leases {
		observation := managed.observation
		observation.Protocols = append([]provider.PortForwardProtocol(nil), observation.Protocols...)
		if observation.Ready && !manager.now().Before(observation.ExpiresAt) {
			observation.Ready = false
			observation.Message = "Provider mapping observation has expired"
		}
		observations = append(observations, observation)
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].Identity < observations[j].Identity })
	return observations
}

func (manager *PortForwardManager) Capabilities() (provider.PortForwardCapabilities, bool, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	capabilities := manager.capabilities
	capabilities.Protocols = append([]provider.PortForwardProtocol(nil), capabilities.Protocols...)
	return capabilities, manager.capabilitiesObserved, manager.capabilityObservationErr
}

func providerRequest(intent PortForwardLeaseIntent, suggestedExternalPort uint16) provider.PortForwardLeaseRequest {
	return provider.PortForwardLeaseRequest{Identity: intent.Identity, InternalPort: intent.InternalPort, SuggestedExternalPort: suggestedExternalPort, Protocols: append([]provider.PortForwardProtocol(nil), intent.Protocols...)}
}

func (manager *PortForwardManager) now() time.Time {
	if manager.Now != nil {
		return manager.Now().UTC()
	}
	return time.Now().UTC()
}
