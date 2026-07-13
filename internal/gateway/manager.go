// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"sync"

	"github.com/Amoenus/waycloak/internal/provider"
)

type HealthManager struct {
	Engine provider.VPNEngine
	Source DesiredSource

	mu          sync.RWMutex
	observation provider.EngineObservation
	err         error
	configErr   error
}

func (manager *HealthManager) Reconcile(ctx context.Context) {
	var configErr error
	if manager.Source != nil {
		_, configErr = manager.Source.Load()
	}
	observation, err := manager.Engine.Observe(ctx)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.observation = observation
	manager.err = err
	manager.configErr = configErr
}

func (manager *HealthManager) Ready() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.err == nil && manager.configErr == nil && manager.observation.TunnelReady && manager.observation.DNSReady && manager.observation.PublicIP.IsValid()
}

func (manager *HealthManager) Error() error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.configErr != nil {
		return manager.configErr
	}
	return manager.err
}
