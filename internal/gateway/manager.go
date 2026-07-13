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

	mu          sync.RWMutex
	observation provider.EngineObservation
	err         error
}

func (manager *HealthManager) Reconcile(ctx context.Context) {
	observation, err := manager.Engine.Observe(ctx)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.observation = observation
	manager.err = err
}

func (manager *HealthManager) Ready() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.err == nil && manager.observation.TunnelReady && manager.observation.DNSReady && manager.observation.PublicIP.IsValid()
}

func (manager *HealthManager) Error() error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.err
}
