// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/provider"
)

type GatewayRule struct {
	LeaseUID             wayv1.ObjectUID
	HandoffGeneration    int64
	ProviderInternalPort uint16
	OverlayAddress       string
	TargetPort           uint16
	Protocols            []wayv1.TransportProtocol
}

type RuleBackend interface {
	Replace(context.Context, []GatewayRule) error
	Ready(context.Context, GatewayRule) (bool, error)
	Withdrawn(context.Context, wayv1.ObjectUID, int64) (bool, error)
}

type DeliveryBackend interface {
	Reconcile(context.Context, Intent, ProviderObservation) (delivered bool, acknowledged bool, err error)
	Withdraw(context.Context, WithdrawalIntent) (bool, error)
}

// GatewayRuntimeManager is tokenless. It owns provider acquisition and the
// complete atomic gateway rule/delivery set, while the Kubernetes controller
// remains the sole status writer.
type GatewayRuntimeManager struct {
	PortForward provider.PortForwardCapability
	Rules       RuleBackend
	Delivery    DeliveryBackend
	Now         func() time.Time

	mu     sync.Mutex
	states map[wayv1.ObjectUID]runtimeState
}

type runtimeState struct {
	intent     Intent
	mapping    ProviderObservation
	renewAfter time.Time
	draining   bool
	drained    bool
	blocked    bool
}

func (m *GatewayRuntimeManager) Reconcile(ctx context.Context, gateway *wayv1.VPNGateway, intent Intent) (Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateRuntimeIntent(gateway, intent); err != nil {
		return Observation{}, err
	}
	if m.PortForward == nil || m.Rules == nil || m.Delivery == nil {
		return Observation{}, errors.New("engine port-forward capability, atomic rules, and delivery backends are required")
	}
	if m.states == nil {
		m.states = map[wayv1.ObjectUID]runtimeState{}
	}
	previous, exists := m.states[intent.LeaseUID]
	if exists {
		if intent.HandoffGeneration < previous.intent.HandoffGeneration {
			return Observation{}, errors.New("handoff generation regressed")
		}
		if intent.HandoffGeneration == previous.intent.HandoffGeneration && !sameRuntimeTarget(previous.intent, intent) {
			return Observation{}, errors.New("target identity changed within one handoff generation")
		}
		if intent.HandoffGeneration > previous.intent.HandoffGeneration && !previous.drained {
			return Observation{}, errors.New("successor target arrived before old rules were withdrawn")
		}
	}
	capabilities, capabilityErr := m.PortForward.ObserveCapabilities(ctx)
	if capabilityErr != nil {
		if !exists || previous.blocked || !previous.mapping.ExpiresAt.After(m.now()) {
			return Observation{}, fmt.Errorf("observe provider capabilities: %w", capabilityErr)
		}
	} else if err := m.validateProviderCapabilities(intent, capabilities, exists); err != nil {
		if exists {
			previous.blocked = true
			m.states[intent.LeaseUID] = previous
			if replaceErr := m.Rules.Replace(ctx, m.ruleSet()); replaceErr != nil {
				return Observation{}, errors.Join(err, fmt.Errorf("withdraw rules after provider capability regression: %w", replaceErr))
			}
		}
		return Observation{}, err
	}
	request, err := providerRequest(intent)
	if err != nil {
		return Observation{}, err
	}
	var mapping ProviderObservation
	var renewAfter time.Time
	now := m.now()
	providerRefreshDue := !exists || previous.renewAfter.IsZero() || !now.Before(previous.renewAfter) || !previous.mapping.ExpiresAt.After(now)
	var providerErr error
	if !providerRefreshDue {
		mapping = previous.mapping
		renewAfter = previous.renewAfter
	} else {
		providerLease, ensureErr := m.PortForward.EnsureLease(ctx, request)
		providerErr = ensureErr
		if providerErr == nil {
			mapping = ProviderObservation{PublicAddress: providerLease.PublicAddress, PublicPort: providerLease.PublicPort, ExpiresAt: providerLease.ExpiresAt.UTC()}
			renewAfter = providerLease.RenewAfter.UTC()
			if !renewAfter.After(now) || !renewAfter.Before(mapping.ExpiresAt) {
				providerErr = errors.New("provider returned an invalid renewal schedule")
			}
		}
		if providerErr != nil {
			if exists && previous.mapping.ExpiresAt.After(now) {
				mapping = previous.mapping
				renewAfter = previous.renewAfter
			} else {
				delete(m.states, intent.LeaseUID)
				_ = m.Rules.Replace(ctx, m.ruleSet())
				return Observation{}, fmt.Errorf("ensure provider mapping: %w", providerErr)
			}
		}
	}
	state := runtimeState{intent: intent, mapping: mapping, renewAfter: renewAfter}
	if exists && intent.HandoffGeneration == previous.intent.HandoffGeneration {
		state.draining = previous.draining
		state.drained = previous.drained
	}
	m.states[intent.LeaseUID] = state
	rule := ruleFor(state)
	if err := m.Rules.Replace(ctx, m.ruleSet()); err != nil {
		if exists {
			m.states[intent.LeaseUID] = previous
		} else {
			delete(m.states, intent.LeaseUID)
		}
		return m.observation(intent, mapping), fmt.Errorf("replace atomic gateway rules: %w", err)
	}
	rulesReady, err := m.Rules.Ready(ctx, rule)
	if err != nil || !rulesReady {
		return m.observation(intent, mapping), err
	}
	delivered, acknowledged, err := m.Delivery.Reconcile(ctx, effectiveIntent(state), mapping)
	observation := m.observation(intent, mapping)
	observation.GatewayRulesReady = true
	observation.Delivered = delivered
	observation.Acknowledged = acknowledged
	return observation, err
}

func (m *GatewayRuntimeManager) validateProviderCapabilities(intent Intent, capabilities provider.PortForwardCapabilities, exists bool) error {
	if capabilities.MaxLeases < 1 {
		return errors.New("provider reports no port-forward lease capacity")
	}
	if len(intent.Protocols) > 1 && !capabilities.SharedPort {
		return errors.New("provider cannot map requested protocols to one public port")
	}
	supported := make(map[provider.PortForwardProtocol]struct{}, len(capabilities.Protocols))
	for _, protocol := range capabilities.Protocols {
		supported[protocol] = struct{}{}
	}
	request, err := providerRequest(intent)
	if err != nil {
		return err
	}
	for _, protocol := range request.Protocols {
		if _, ok := supported[protocol]; !ok {
			return fmt.Errorf("provider does not support requested %s mapping", protocol)
		}
	}
	if !exists {
		active := int32(0)
		for _, state := range m.states {
			if state.mapping.ExpiresAt.After(m.now()) {
				active++
			}
		}
		if active >= capabilities.MaxLeases {
			return errors.New("provider port-forward lease capacity is exhausted")
		}
	}
	return nil
}

func (m *GatewayRuntimeManager) Withdraw(ctx context.Context, gateway *wayv1.VPNGateway, intent WithdrawalIntent) (Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateWithdrawal(gateway, intent); err != nil {
		return Observation{}, err
	}
	if m.Rules == nil || m.Delivery == nil {
		return Observation{}, errors.New("atomic rules and delivery backends are required")
	}
	state, exists := m.states[intent.LeaseUID]
	if exists && state.intent.HandoffGeneration != intent.HandoffGeneration {
		return Observation{}, errors.New("withdrawal generation does not match active state")
	}
	if exists && !state.draining && !state.drained {
		state.draining = true
		m.states[intent.LeaseUID] = state
	}
	if err := m.Rules.Replace(ctx, m.ruleSet()); err != nil {
		if exists {
			state.draining = false
			m.states[intent.LeaseUID] = state
		}
		return m.withdrawalObservation(intent, false), err
	}
	rulesWithdrawn, err := m.Rules.Withdrawn(ctx, intent.LeaseUID, intent.HandoffGeneration)
	if err != nil || !rulesWithdrawn {
		return m.withdrawalObservation(intent, false), err
	}
	deliveryWithdrawn, err := m.Delivery.Withdraw(ctx, intent)
	if err != nil || !deliveryWithdrawn {
		return m.withdrawalObservation(intent, false), err
	}
	if exists {
		state.draining = false
		state.drained = true
		m.states[intent.LeaseUID] = state
	}
	if intent.ReleaseProvider {
		releaseIntent := Intent{LeaseUID: intent.LeaseUID, ProviderInternalPort: intent.ProviderInternalPort, PreviousPublicPort: intent.PreviousPublicPort, Protocols: intent.Protocols}
		if exists {
			releaseIntent = state.intent
		}
		request, requestErr := providerRequest(releaseIntent)
		if requestErr != nil {
			return m.withdrawalObservation(intent, false), requestErr
		}
		if m.PortForward == nil {
			return m.withdrawalObservation(intent, false), errors.New("engine port-forward capability is unavailable")
		}
		if err := m.PortForward.ReleaseLease(ctx, request); err != nil {
			return m.withdrawalObservation(intent, false), err
		}
		delete(m.states, intent.LeaseUID)
	}
	return m.withdrawalObservation(intent, true), nil
}

func (m *GatewayRuntimeManager) Quarantine(context.Context, *wayv1.VPNGateway, WithdrawalIntent, time.Time) error {
	// The Kubernetes-side durable provider-port reservation carries the bounded
	// quarantine. The tokenless runtime retains no reusable numeric allocator.
	return nil
}

func (m *GatewayRuntimeManager) ruleSet() []GatewayRule {
	rules := make([]GatewayRule, 0, len(m.states))
	for _, state := range m.states {
		if !state.draining && !state.drained && !state.blocked && state.mapping.ExpiresAt.After(m.now()) {
			rules = append(rules, ruleFor(state))
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].LeaseUID < rules[j].LeaseUID })
	return rules
}

func ruleFor(state runtimeState) GatewayRule {
	return GatewayRule{LeaseUID: state.intent.LeaseUID, HandoffGeneration: state.intent.HandoffGeneration, ProviderInternalPort: state.intent.ProviderInternalPort,
		OverlayAddress: state.intent.OverlayAddress.String(), TargetPort: effectiveTargetPort(state), Protocols: append([]wayv1.TransportProtocol(nil), state.intent.Protocols...)}
}

func effectiveIntent(state runtimeState) Intent {
	intent := state.intent
	intent.TargetPort = effectiveTargetPort(state)
	return intent
}

func effectiveTargetPort(state runtimeState) uint16 {
	if state.intent.ApplicationPortMode == ApplicationPortProviderAssigned {
		return state.mapping.PublicPort
	}
	return state.intent.TargetPort
}

func (m *GatewayRuntimeManager) observation(intent Intent, mapping ProviderObservation) Observation {
	return Observation{APIVersion: RuntimeAPIVersion, LeaseUID: intent.LeaseUID, GatewayUID: intent.GatewayUID, HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID, ObservedAt: m.now(), Provider: &mapping}
}

func (m *GatewayRuntimeManager) withdrawalObservation(intent WithdrawalIntent, withdrawn bool) Observation {
	return Observation{APIVersion: RuntimeAPIVersion, LeaseUID: intent.LeaseUID, GatewayUID: intent.GatewayUID, HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID, ObservedAt: m.now(), Withdrawn: withdrawn}
}

func providerRequest(intent Intent) (provider.PortForwardLeaseRequest, error) {
	protocols := make([]provider.PortForwardProtocol, 0, len(intent.Protocols))
	for _, protocol := range intent.Protocols {
		switch protocol {
		case wayv1.ProtocolTCP:
			protocols = append(protocols, provider.ProtocolTCP)
		case wayv1.ProtocolUDP:
			protocols = append(protocols, provider.ProtocolUDP)
		default:
			return provider.PortForwardLeaseRequest{}, errors.New("unsupported transport protocol")
		}
	}
	return provider.PortForwardLeaseRequest{Identity: string(intent.LeaseUID), InternalPort: intent.ProviderInternalPort, SuggestedExternalPort: intent.PreviousPublicPort, Protocols: protocols}, nil
}

func validateRuntimeIntent(gateway *wayv1.VPNGateway, intent Intent) error {
	if gateway == nil || gateway.UID == "" || intent.APIVersion != RuntimeAPIVersion || intent.LeaseNamespace == "" || intent.LeaseUID == "" || intent.GatewayUID != wayv1.ObjectUID(gateway.UID) ||
		intent.HandoffGeneration < 1 || intent.ServiceUID == "" || intent.EndpointSliceUID == "" || intent.PodUID == "" || intent.BindingUID == "" ||
		intent.ProviderInternalPort < ProviderPortFirst || intent.BackendPort == 0 || intent.TargetPort == 0 || !intent.OverlayAddress.Is4() || !intent.ApplicationAddress.Is4() || len(intent.Protocols) == 0 ||
		(intent.ApplicationPortMode != ApplicationPortFixed && intent.ApplicationPortMode != ApplicationPortProviderAssigned) {
		return errors.New("port-forward runtime intent is invalid")
	}
	if _, err := normalizedProtocols(intent.Protocols); err != nil {
		return err
	}
	return nil
}

func validateWithdrawal(gateway *wayv1.VPNGateway, intent WithdrawalIntent) error {
	if gateway == nil || gateway.UID == "" || intent.APIVersion != RuntimeAPIVersion || intent.LeaseNamespace == "" || intent.LeaseUID == "" || intent.GatewayUID != wayv1.ObjectUID(gateway.UID) || intent.HandoffGeneration < 1 || intent.PodUID == "" ||
		(intent.ReleaseProvider && (intent.ProviderInternalPort < ProviderPortFirst || len(intent.Protocols) == 0)) {
		return errors.New("port-forward withdrawal intent is invalid")
	}
	if intent.ReleaseProvider {
		if _, err := normalizedProtocols(intent.Protocols); err != nil {
			return err
		}
	}
	return nil
}

func sameRuntimeTarget(a, b Intent) bool {
	aProtocols, aErr := normalizedProtocols(a.Protocols)
	bProtocols, bErr := normalizedProtocols(b.Protocols)
	return a.LeaseUID == b.LeaseUID && a.GatewayUID == b.GatewayUID && a.ServiceUID == b.ServiceUID && a.PodUID == b.PodUID && a.BindingUID == b.BindingUID &&
		a.ProviderInternalPort == b.ProviderInternalPort && a.OverlayAddress == b.OverlayAddress && a.ApplicationAddress == b.ApplicationAddress && a.BackendPort == b.BackendPort && a.TargetPort == b.TargetPort && a.ApplicationPortMode == b.ApplicationPortMode &&
		aErr == nil && bErr == nil && slices.Equal(aProtocols, bProtocols) && a.AdapterName == b.AdapterName
}

func normalizedProtocols(protocols []wayv1.TransportProtocol) ([]wayv1.TransportProtocol, error) {
	if len(protocols) == 0 || len(protocols) > 2 {
		return nil, errors.New("port-forward runtime requires one or two transport protocols")
	}
	result := append([]wayv1.TransportProtocol(nil), protocols...)
	slices.Sort(result)
	for index, protocol := range result {
		if protocol != wayv1.ProtocolTCP && protocol != wayv1.ProtocolUDP {
			return nil, errors.New("port-forward runtime protocol is unsupported")
		}
		if index > 0 && result[index-1] == protocol {
			return nil, errors.New("port-forward runtime protocols must be unique")
		}
	}
	return result, nil
}

func (m *GatewayRuntimeManager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

var _ Runtime = (*GatewayRuntimeManager)(nil)
