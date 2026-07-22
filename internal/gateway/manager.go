// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"

	"github.com/Amoenus/waycloak/internal/provider"
)

type HealthManager struct {
	Engine         provider.VPNEngine
	Source         DesiredSource
	Network        Network
	Forwarding     Forwarding
	DNS            DNSService
	PortForwarding *PortForwardManager
	Logger         *slog.Logger

	mu                          sync.RWMutex
	observation                 provider.EngineObservation
	err                         error
	configErr                   error
	networkErr                  error
	forwardingErr               error
	dnsErr                      error
	portForwardingErr           error
	portForwardRulesErr         error
	portForwardRules            map[string]PortForwardRuleObservation
	appliedMembershipGeneration string

	lastDesired          *DesiredState
	healthObserved       bool
	previouslyReady      bool
	previousHealthReason string
	configSourceObserved bool
	usingRetainedDesired bool
	previousGeneration   string
}

func (manager *HealthManager) Reconcile(ctx context.Context) {
	var configErr, networkErr, forwardingErr, dnsErr, portForwardRulesErr error
	var portForwardRules []PortForwardRuleObservation
	var desired DesiredState
	usingRetainedDesired := false
	if manager.Source != nil {
		desired, configErr = manager.Source.Load()
		if configErr != nil && errors.Is(configErr, os.ErrNotExist) && manager.lastDesired != nil {
			desired = *manager.lastDesired
			configErr = nil
			usingRetainedDesired = true
		}
		if configErr == nil {
			manager.lastDesired = &desired
			if manager.Forwarding != nil {
				forwardingErr = manager.Forwarding.InstallLockdown(ctx, desired)
			}
			if forwardingErr == nil && manager.Network != nil {
				networkErr = manager.Network.Reconcile(ctx, desired)
			}
		}
	}
	observation, err := manager.Engine.Observe(ctx)
	var portForwardingErr error
	if manager.PortForwarding != nil && configErr == nil && err == nil && observation.TunnelReady {
		portForwardingErr = manager.PortForwarding.Reconcile(ctx, desired.PortForwardLeases)
	}
	effectiveDesired := desired
	if manager.PortForwarding != nil {
		effectiveDesired.PortForwardLeases = effectivePortForwardLeases(desired.PortForwardLeases, manager.PortForwarding.Snapshot(), err == nil && observation.TunnelReady)
	}
	if configErr == nil && forwardingErr == nil && networkErr == nil && manager.Forwarding != nil {
		forwardingErr = manager.Forwarding.Reconcile(ctx, effectiveDesired)
		if forwardingErr == nil {
			portForwardRules, portForwardRulesErr = manager.Forwarding.ObservePortForwardRules(ctx, effectiveDesired)
		}
	}
	if configErr == nil && networkErr == nil && forwardingErr == nil && manager.DNS != nil {
		dnsErr = manager.DNS.Reconcile(ctx, desired)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.observation = observation
	manager.err = err
	manager.configErr = configErr
	manager.networkErr = networkErr
	manager.forwardingErr = forwardingErr
	manager.dnsErr = dnsErr
	manager.portForwardingErr = portForwardingErr
	manager.portForwardRulesErr = portForwardRulesErr
	manager.portForwardRules = make(map[string]PortForwardRuleObservation, len(portForwardRules))
	for _, rule := range portForwardRules {
		manager.portForwardRules[rule.Identity] = rule
	}
	if manager.Source != nil && desired.MembershipGeneration != "" && configErr == nil && networkErr == nil && forwardingErr == nil && dnsErr == nil && portForwardRulesErr == nil {
		manager.appliedMembershipGeneration = desired.MembershipGeneration
	}
	ready, healthReason := healthState(observation, configErr, networkErr, forwardingErr, dnsErr, portForwardingErr, portForwardRulesErr, err)
	if !manager.healthObserved || manager.previouslyReady != ready || manager.previousHealthReason != healthReason {
		manager.logHealthTransition(ready, healthReason, desired, observation, configErr, networkErr, forwardingErr, dnsErr, portForwardingErr, portForwardRulesErr, err)
	}
	manager.healthObserved = true
	manager.previouslyReady = ready
	manager.previousHealthReason = healthReason
	if manager.Source != nil && (!manager.configSourceObserved || manager.usingRetainedDesired != usingRetainedDesired || manager.previousGeneration != desired.MembershipGeneration) {
		manager.logger().Info("gateway desired-state source transitioned", "event", "gateway_config_source_transition", "using_retained", usingRetainedDesired, "membership_generation", desired.MembershipGeneration)
	}
	manager.configSourceObserved = true
	manager.usingRetainedDesired = usingRetainedDesired
	manager.previousGeneration = desired.MembershipGeneration
}

func healthReady(observation provider.EngineObservation, configErr, networkErr, forwardingErr, dnsErr, portForwardingErr, portForwardRulesErr, engineErr error) bool {
	ready, _ := healthState(observation, configErr, networkErr, forwardingErr, dnsErr, portForwardingErr, portForwardRulesErr, engineErr)
	return ready
}

func healthState(observation provider.EngineObservation, configErr, networkErr, forwardingErr, dnsErr, portForwardingErr, portForwardRulesErr, engineErr error) (bool, string) {
	for _, failure := range []struct {
		reason string
		err    error
	}{
		{reason: "config-error", err: configErr},
		{reason: "network-error", err: networkErr},
		{reason: "forwarding-error", err: forwardingErr},
		{reason: "dns-error", err: dnsErr},
		{reason: "port-forwarding-error", err: portForwardingErr},
		{reason: "port-forward-rules-error", err: portForwardRulesErr},
		{reason: "engine-error", err: engineErr},
	} {
		if failure.err != nil {
			return false, failure.reason
		}
	}
	if !observation.TunnelReady {
		return false, "tunnel-not-ready"
	}
	if !observation.DNSReady {
		return false, "engine-dns-not-ready"
	}
	return true, "ready"
}

func (manager *HealthManager) logHealthTransition(ready bool, reason string, desired DesiredState, observation provider.EngineObservation, configErr, networkErr, forwardingErr, dnsErr, portForwardingErr, portForwardRulesErr, engineErr error) {
	manager.logger().Info("gateway health transitioned",
		"event", "gateway_health_transition",
		"ready", ready,
		"reason", reason,
		"membership_generation", desired.MembershipGeneration,
		"tunnel_ready", observation.TunnelReady,
		"dns_ready", observation.DNSReady,
		"config_error", errorText(configErr),
		"network_error", errorText(networkErr),
		"forwarding_error", errorText(forwardingErr),
		"dns_error", errorText(dnsErr),
		"port_forwarding_error", errorText(portForwardingErr),
		"port_forward_rules_error", errorText(portForwardRulesErr),
		"engine_error", errorText(engineErr),
	)
}

func (manager *HealthManager) logger() *slog.Logger {
	if manager.Logger != nil {
		return manager.Logger
	}
	return slog.Default()
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func effectivePortForwardLeases(desired []PortForwardLeaseIntent, observations []PortForwardObservation, tunnelReady bool) []PortForwardLeaseIntent {
	observed := make(map[string]PortForwardObservation, len(observations))
	for i := range observations {
		observed[observations[i].Identity] = observations[i]
	}
	effective := make([]PortForwardLeaseIntent, 0, len(desired))
	for i := range desired {
		intent := desired[i]
		intent.LeaseGeneration = 0
		intent.SuggestedExternalPort = 0
		observation, exists := observed[intent.Identity]
		if tunnelReady && exists && observation.Ready && observation.LeaseGeneration > 0 {
			intent.LeaseGeneration = observation.LeaseGeneration
			intent.SuggestedExternalPort = observation.PublicPort
		}
		effective = append(effective, intent)
	}
	return effective
}

func (manager *HealthManager) AppliedMembershipGeneration() string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.appliedMembershipGeneration
}

func (manager *HealthManager) PortForwardingError() error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return errors.Join(manager.portForwardingErr, manager.portForwardRulesErr)
}

func (manager *HealthManager) PortForwardingSnapshot() []PortForwardObservation {
	if manager.PortForwarding == nil {
		return nil
	}
	observations := manager.PortForwarding.Snapshot()
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.err != nil || !manager.observation.TunnelReady {
		for i := range observations {
			observations[i].Ready = false
			observations[i].GatewayRulesReady = false
			observations[i].Message = "VPN tunnel is not observed ready"
		}
		return observations
	}
	if manager.portForwardRulesErr != nil || manager.forwardingErr != nil {
		return observations
	}
	for i := range observations {
		rule, exists := manager.portForwardRules[observations[i].Identity]
		if !exists || !rule.Ready || rule.LeaseGeneration != observations[i].LeaseGeneration {
			continue
		}
		observations[i].GatewayRulesReady = true
		observations[i].GatewayRulesGeneration = rule.LeaseGeneration
		observations[i].TargetAddress = rule.TargetAddress
		observations[i].TargetPort = rule.TargetPort
	}
	return observations
}

func (manager *HealthManager) Ready() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return healthReady(manager.observation, manager.configErr, manager.networkErr, manager.forwardingErr, manager.dnsErr, manager.portForwardingErr, manager.portForwardRulesErr, manager.err)
}

func (manager *HealthManager) Error() error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.configErr != nil {
		return manager.configErr
	}
	if manager.networkErr != nil {
		return manager.networkErr
	}
	if manager.forwardingErr != nil {
		return manager.forwardingErr
	}
	if manager.dnsErr != nil {
		return manager.dnsErr
	}
	return manager.err
}
