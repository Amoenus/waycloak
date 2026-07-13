// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/dataplane"
)

type loopBackend struct{ repairs int }

func (*loopBackend) Preflight(context.Context) error                   { return nil }
func (*loopBackend) InstallLockdown(context.Context, string) error     { return nil }
func (*loopBackend) Configure(context.Context, dataplane.Config) error { return nil }
func (*loopBackend) Verify(context.Context, dataplane.Config) error    { return nil }
func (b *loopBackend) Repair(context.Context, dataplane.Config) error  { b.repairs++; return nil }

func TestReconcileLoopRetriesAfterLoadFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	b := &loopBackend{}
	loads := 0
	load := func() (dataplane.Config, error) {
		loads++
		if loads == 1 {
			return dataplane.Config{}, errors.New("transient projection update")
		}
		return dataplane.Config{PodUID: "uid", Address: netip.MustParsePrefix("172.30.99.2/24"), OverlayCIDR: netip.MustParsePrefix("172.30.99.0/24"), GatewayAddress: netip.MustParseAddr("172.30.99.1"), GatewayEndpoint: netip.MustParseAddrPort("10.0.0.2:4789"), GatewayHealthPort: 18080, VNI: 7999, MTU: 1320, ClusterTrafficMode: dataplane.ClusterTrafficPreserve}, nil
	}
	ready := &atomic.Bool{}
	if err := reconcileLoop(ctx, dataplane.Agent{Backend: b}, load, time.Millisecond, ready); err != nil {
		t.Fatal(err)
	}
	if b.repairs == 0 {
		t.Fatal("reconcile loop did not recover from load failure")
	}
	if !ready.Load() {
		t.Fatal("readiness did not recover")
	}
}

func TestReadinessHandler(t *testing.T) {
	ready := &atomic.Bool{}
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	readinessHandler(ready).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready status = %d", response.Code)
	}
	ready.Store(true)
	response = httptest.NewRecorder()
	readinessHandler(ready).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ready status = %d", response.Code)
	}
}
