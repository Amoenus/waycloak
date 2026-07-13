// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/Amoenus/waycloak/internal/dataplane"
	"github.com/Amoenus/waycloak/internal/delivery"
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

func TestLocalReadinessProbeRequiresHTTP200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			t.Fatalf("probe path = %q", request.URL.Path)
		}
		http.Error(response, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := probeReadiness(context.Background(), server.URL+"/readyz"); err == nil {
		t.Fatal("unready HTTP response passed the local probe")
	}
	readyServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) }))
	defer readyServer.Close()
	if err := probeReadiness(context.Background(), readyServer.URL); err != nil {
		t.Fatalf("ready HTTP response failed the local probe: %v", err)
	}
}

func TestLeaseHandlersExposeValidatedReadOnlyPodRecord(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	document := delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicPort: 42000, TargetPort: 6881, Protocols: []string{"TCP", "UDP"}, Generation: 4, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
	serialized, err := delivery.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, contract.PortForwardLeasesKey), []byte(serialized), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &delivery.Store{Now: func() time.Time { return now }}
	if err := store.Refresh(directory); err != nil {
		t.Fatal(err)
	}
	ready := &atomic.Bool{}
	for _, test := range []struct {
		handler http.Handler
		path    string
	}{
		{handler: agentHandler(ready, store), path: "/v1/port-forward/deliveries/lease-uid"},
		{handler: leaseHandler(store), path: "/v1/port-forward/leases"},
		{handler: leaseHandler(store), path: "/v1/port-forward/leases/lease-uid"},
	} {
		response := httptest.NewRecorder()
		test.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d: %s", test.path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	leaseHandler(store).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/port-forward/leases/lease-uid", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write method returned %d", response.Code)
	}
}
