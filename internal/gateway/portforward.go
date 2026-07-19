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
	Identity               string                         `json:"identity"`
	InternalPort           uint16                         `json:"internalPort"`
	Protocols              []provider.PortForwardProtocol `json:"protocols"`
	PublicAddress          string                         `json:"publicAddress,omitempty"`
	PublicPort             uint16                         `json:"publicPort,omitempty"`
	IssuedAt               time.Time                      `json:"issuedAt,omitempty"`
	RenewAfter             time.Time                      `json:"renewAfter,omitempty"`
	ExpiresAt              time.Time                      `json:"expiresAt,omitempty"`
	LeaseGeneration        int64                          `json:"leaseGeneration,omitempty"`
	Ready                  bool                           `json:"ready"`
	RenewalPending         bool                           `json:"renewalPending,omitempty"`
	GatewayRulesReady      bool                           `json:"gatewayRulesReady"`
	GatewayRulesGeneration int64                          `json:"gatewayRulesGeneration,omitempty"`
	TargetAddress          string                         `json:"targetAddress,omitempty"`
	TargetPort             uint16                         `json:"targetPort,omitempty"`
	Releasing              bool                           `json:"releasing,omitempty"`
	Message                string                         `json:"message,omitempty"`
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

	reconcileMu              sync.Mutex
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
	manager.reconcileMu.Lock()
	defer manager.reconcileMu.Unlock()

	manager.mu.RLock()
	leases := cloneManagedPortForwardLeases(manager.leases)
	capabilities := clonePortForwardCapabilities(manager.capabilities)
	capabilitiesObserved := manager.capabilitiesObserved
	capabilityObservationErr := manager.capabilityObservationErr
	manager.mu.RUnlock()
	if leases == nil {
		leases = map[string]managedPortForwardLease{}
	}
	if !capabilitiesObserved {
		capabilities, capabilityObservationErr = manager.Driver.ObserveCapabilities(ctx)
		capabilitiesObserved = capabilityObservationErr == nil
		manager.mu.Lock()
		manager.capabilities = clonePortForwardCapabilities(capabilities)
		manager.capabilitiesObserved = capabilitiesObserved
		manager.capabilityObservationErr = capabilityObservationErr
		manager.mu.Unlock()
		if capabilityObservationErr != nil {
			return capabilityObservationErr
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
	for identity, managed := range leases {
		if _, exists := wanted[identity]; exists {
			continue
		}
		managed.observation.Ready = false
		managed.observation.Releasing = true
		managed.observation.Message = "Provider mapping release is pending"
		leases[identity] = managed
		manager.publishState(leases, capabilities, capabilitiesObserved, capabilityObservationErr)
		request := providerRequest(managed.intent, managed.observation.PublicPort)
		if err := manager.Driver.ReleaseLease(ctx, request); err != nil {
			managed.observation.Message = "Provider mapping release failed: " + err.Error()
			leases[identity] = managed
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		delete(leases, identity)
	}

	now := manager.now()
	for _, intent := range desired {
		managed, exists := leases[intent.Identity]
		if exists && intent.LeaseGeneration > managed.observation.LeaseGeneration {
			managed.observation.LeaseGeneration = intent.LeaseGeneration
			leases[intent.Identity] = managed
		}
		if exists && intent.LeaseGeneration > 0 && intent.LeaseGeneration >= managed.observation.LeaseGeneration &&
			((intent.SuggestedExternalAddress != "" && intent.SuggestedExternalAddress != managed.observation.PublicAddress) ||
				(intent.SuggestedExternalPort != 0 && intent.SuggestedExternalPort != managed.observation.PublicPort)) {
			managed.observation.LeaseGeneration = intent.LeaseGeneration + 1
			leases[intent.Identity] = managed
		}
		intentChanged := exists && !reflect.DeepEqual(providerRequest(managed.intent, 0), providerRequest(intent, 0))
		if intentChanged {
			managed.observation.Ready = false
			managed.observation.Releasing = true
			managed.observation.Message = "Provider mapping replacement is pending"
			leases[intent.Identity] = managed
			manager.publishState(leases, capabilities, capabilitiesObserved, capabilityObservationErr)
			if err := manager.Driver.ReleaseLease(ctx, providerRequest(managed.intent, managed.observation.PublicPort)); err != nil {
				managed.observation.Message = "Previous provider mapping release failed: " + err.Error()
				leases[intent.Identity] = managed
				reconcileErr = errors.Join(reconcileErr, err)
				continue
			}
			exists = false
		}
		needsEnsure := !exists || !managed.observation.Ready || !now.Before(managed.observation.RenewAfter)
		if !needsEnsure {
			managed.intent = intent
			leases[intent.Identity] = managed
			continue
		}
		var suggestedPort uint16
		if capabilities.SupportsRequestedPort {
			suggestedPort = intent.SuggestedExternalPort
			if exists && managed.observation.PublicPort != 0 {
				suggestedPort = managed.observation.PublicPort
			}
		}
		observation, err := manager.Driver.EnsureLease(ctx, providerRequest(intent, suggestedPort))
		if err == nil && !validPortForwardLeaseObservation(observation, now) {
			err = errors.New("provider returned an invalid port-forward lease observation")
		}
		if err != nil {
			if exists && managed.observation.Ready && now.Before(managed.observation.ExpiresAt) {
				managed.intent = intent
				managed.observation.RenewalPending = true
				managed.observation.Message = "Provider mapping renewal is pending; the last observation remains current: " + err.Error()
				leases[intent.Identity] = managed
				continue
			}
			if !exists || !now.Before(managed.observation.ExpiresAt) {
				managed.observation = PortForwardObservation{Identity: intent.Identity, InternalPort: intent.InternalPort, Protocols: append([]provider.PortForwardProtocol(nil), intent.Protocols...), LeaseGeneration: max(managed.observation.LeaseGeneration, intent.LeaseGeneration)}
			}
			managed.intent = intent
			managed.observation.RenewalPending = true
			managed.observation.Message = "Provider mapping renewal failed: " + err.Error()
			leases[intent.Identity] = managed
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		previousAddress := managed.observation.PublicAddress
		previousPort := managed.observation.PublicPort
		leaseGeneration := max(managed.observation.LeaseGeneration, intent.LeaseGeneration)
		if leaseGeneration == 0 || previousAddress != observation.PublicAddress.String() || previousPort != observation.PublicPort {
			leaseGeneration++
		}
		managed.intent = intent
		managed.observation = PortForwardObservation{Identity: intent.Identity, InternalPort: intent.InternalPort, Protocols: append([]provider.PortForwardProtocol(nil), intent.Protocols...), LeaseGeneration: leaseGeneration}
		managed.observation.PublicAddress = observation.PublicAddress.String()
		managed.observation.PublicPort = observation.PublicPort
		managed.observation.IssuedAt = observation.IssuedAt
		managed.observation.RenewAfter = observation.RenewAfter
		managed.observation.ExpiresAt = observation.ExpiresAt
		managed.observation.Ready = true
		managed.observation.Message = "Provider mapping is current"
		leases[intent.Identity] = managed
	}
	manager.publishState(leases, capabilities, capabilitiesObserved, capabilityObservationErr)
	return reconcileErr
}

func validPortForwardLeaseObservation(observation provider.PortForwardLeaseObservation, now time.Time) bool {
	return observation.PublicAddress.Is4() && observation.PublicAddress.IsGlobalUnicast() && observation.PublicPort != 0 &&
		!observation.IssuedAt.IsZero() && !observation.RenewAfter.IsZero() && !observation.ExpiresAt.IsZero() &&
		observation.IssuedAt.Before(observation.ExpiresAt) && observation.RenewAfter.Before(observation.ExpiresAt) && now.Before(observation.ExpiresAt)
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
	capabilities := clonePortForwardCapabilities(manager.capabilities)
	return capabilities, manager.capabilitiesObserved, manager.capabilityObservationErr
}

func cloneManagedPortForwardLeases(source map[string]managedPortForwardLease) map[string]managedPortForwardLease {
	if source == nil {
		return nil
	}
	cloned := make(map[string]managedPortForwardLease, len(source))
	for identity, managed := range source {
		managed.intent.Protocols = append([]provider.PortForwardProtocol(nil), managed.intent.Protocols...)
		managed.observation.Protocols = append([]provider.PortForwardProtocol(nil), managed.observation.Protocols...)
		cloned[identity] = managed
	}
	return cloned
}

func clonePortForwardCapabilities(capabilities provider.PortForwardCapabilities) provider.PortForwardCapabilities {
	capabilities.Protocols = append([]provider.PortForwardProtocol(nil), capabilities.Protocols...)
	return capabilities
}

func (manager *PortForwardManager) publishState(leases map[string]managedPortForwardLease, capabilities provider.PortForwardCapabilities, capabilitiesObserved bool, capabilityObservationErr error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.leases = cloneManagedPortForwardLeases(leases)
	manager.capabilities = clonePortForwardCapabilities(capabilities)
	manager.capabilitiesObserved = capabilitiesObserved
	manager.capabilityObservationErr = capabilityObservationErr
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
