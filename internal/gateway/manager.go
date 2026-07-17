// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
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
}

func (manager *HealthManager) Reconcile(ctx context.Context) {
	var configErr, networkErr, forwardingErr, dnsErr, portForwardRulesErr error
	var portForwardRules []PortForwardRuleObservation
	var desired DesiredState
	if manager.Source != nil {
		desired, configErr = manager.Source.Load()
		if configErr == nil && manager.Forwarding != nil {
			forwardingErr = manager.Forwarding.InstallLockdown(ctx, desired)
		}
		if configErr == nil && forwardingErr == nil && manager.Network != nil {
			networkErr = manager.Network.Reconcile(ctx, desired)
		}
		if configErr == nil && forwardingErr == nil && networkErr == nil && manager.Forwarding != nil {
			forwardingErr = manager.Forwarding.Reconcile(ctx, desired)
			if forwardingErr == nil {
				portForwardRules, portForwardRulesErr = manager.Forwarding.ObservePortForwardRules(ctx, desired)
			}
		}
		if configErr == nil && networkErr == nil && forwardingErr == nil && manager.DNS != nil {
			dnsErr = manager.DNS.Reconcile(ctx, desired)
		}
	}
	observation, err := manager.Engine.Observe(ctx)
	var portForwardingErr error
	if manager.PortForwarding != nil && configErr == nil && err == nil && observation.TunnelReady {
		portForwardingErr = manager.PortForwarding.Reconcile(ctx, desired.PortForwardLeases)
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
		if !exists || !rule.Ready {
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
	return manager.err == nil && manager.configErr == nil && manager.networkErr == nil && manager.forwardingErr == nil && manager.dnsErr == nil && manager.portForwardingErr == nil && manager.portForwardRulesErr == nil && manager.observation.TunnelReady && manager.observation.DNSReady
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
