// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"sync"

	"github.com/Amoenus/waycloak/internal/provider"
)

type HealthManager struct {
	Engine     provider.VPNEngine
	Source     DesiredSource
	Network    Network
	Forwarding Forwarding
	DNS        DNSService

	mu            sync.RWMutex
	observation   provider.EngineObservation
	err           error
	configErr     error
	networkErr    error
	forwardingErr error
	dnsErr        error
}

func (manager *HealthManager) Reconcile(ctx context.Context) {
	var configErr, networkErr, forwardingErr, dnsErr error
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
		}
		if configErr == nil && networkErr == nil && forwardingErr == nil && manager.DNS != nil {
			dnsErr = manager.DNS.Reconcile(ctx, desired)
		}
	}
	observation, err := manager.Engine.Observe(ctx)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.observation = observation
	manager.err = err
	manager.configErr = configErr
	manager.networkErr = networkErr
	manager.forwardingErr = forwardingErr
	manager.dnsErr = dnsErr
}

func (manager *HealthManager) Ready() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.err == nil && manager.configErr == nil && manager.networkErr == nil && manager.forwardingErr == nil && manager.dnsErr == nil && manager.observation.TunnelReady && manager.observation.DNSReady && manager.observation.PublicIP.IsValid()
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
