// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"sync"

	"github.com/Amoenus/waycloak/internal/provider"
)

type HealthManager struct {
	Engine  provider.VPNEngine
	Source  DesiredSource
	Network Network

	mu          sync.RWMutex
	observation provider.EngineObservation
	err         error
	configErr   error
	networkErr  error
}

func (manager *HealthManager) Reconcile(ctx context.Context) {
	var configErr, networkErr error
	var desired DesiredState
	if manager.Source != nil {
		desired, configErr = manager.Source.Load()
		if configErr == nil && manager.Network != nil {
			networkErr = manager.Network.Reconcile(ctx, desired)
		}
	}
	observation, err := manager.Engine.Observe(ctx)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.observation = observation
	manager.err = err
	manager.configErr = configErr
	manager.networkErr = networkErr
}

func (manager *HealthManager) Ready() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.err == nil && manager.configErr == nil && manager.networkErr == nil && manager.observation.TunnelReady && manager.observation.DNSReady && manager.observation.PublicIP.IsValid()
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
	return manager.err
}
