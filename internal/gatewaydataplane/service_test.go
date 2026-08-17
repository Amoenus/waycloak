// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

func TestHealthHandlerSeparatesReadinessAndStatus(t *testing.T) {
	service := &Service{}

	readiness := httptest.NewRecorder()
	service.healthHandler().ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy readiness status = %d", readiness.Code)
	}

	service.healthy.Store(true)
	service.tunnelReady.Store(true)
	service.dnsReady.Store(false)
	readiness = httptest.NewRecorder()
	service.healthHandler().ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusOK {
		t.Fatalf("healthy readiness status = %d", readiness.Code)
	}

	status := httptest.NewRecorder()
	service.healthHandler().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var observed HealthStatus
	if err := json.Unmarshal(status.Body.Bytes(), &observed); err != nil {
		t.Fatal(err)
	}
	if status.Code != http.StatusOK || !observed.Ready || !observed.TunnelReady || observed.DNSReady {
		t.Fatalf("status response = %d %#v", status.Code, observed)
	}

	missing := httptest.NewRecorder()
	service.healthHandler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unrecognized health path status = %d", missing.Code)
	}
}

func TestReconcileDoesNotWithdrawHealthyRulesBetweenObservations(t *testing.T) {
	backend := &recordingBackend{}
	engine := &recordingEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true}}
	service := &Service{Config: testConfig(), Backend: backend, Engine: engine, DNSProber: recordingDNSProber{}}
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(backend.health) != 2 || backend.health[0] || !backend.health[1] {
		t.Fatalf("healthy reconciliation transiently withdrew rules: %#v", backend.health)
	}
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(backend.health) != 3 || !backend.health[2] {
		t.Fatalf("second healthy reconciliation withdrew rules: %#v", backend.health)
	}
}
func TestReconcileWithdrawsRulesBeforeReportingEngineFailure(t *testing.T) {
	backend := &recordingBackend{}
	engine := &recordingEngine{err: errors.New("tunnel down")}
	service := &Service{Config: testConfig(), Backend: backend, Engine: engine, DNSProber: recordingDNSProber{}}
	if err := service.Reconcile(context.Background()); err == nil {
		t.Fatal("engine failure was hidden")
	}
	if len(backend.health) != 1 || backend.health[0] {
		t.Fatalf("engine failure did not install deny state: %#v", backend.health)
	}
}
func TestReconcileReportsUnreadyObservation(t *testing.T) {
	backend := &recordingBackend{}
	engine := &recordingEngine{observation: provider.EngineObservation{TunnelReady: false, DNSReady: true}}
	service := &Service{Config: testConfig(), Backend: backend, Engine: engine, DNSProber: recordingDNSProber{}}
	if err := service.Reconcile(context.Background()); err == nil {
		t.Fatal("unready observation was hidden")
	}
	if len(backend.health) != 1 || backend.health[0] {
		t.Fatalf("unready observation did not install deny state: %#v", backend.health)
	}
}

func TestReconcileWithdrawsRulesWhenSplitDNSProbeFails(t *testing.T) {
	backend := &recordingBackend{}
	engine := &recordingEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true}}
	service := &Service{Config: testConfig(), Backend: backend, Engine: engine, DNSProber: recordingDNSProber{err: errors.New("cluster DNS unavailable")}}
	if err := service.Reconcile(context.Background()); err == nil {
		t.Fatal("split-DNS failure was hidden")
	}
	if len(backend.health) != 2 || backend.health[0] || backend.health[1] {
		t.Fatalf("split-DNS failure did not retain deny state: %#v", backend.health)
	}
	if status := service.HealthStatus(); status.Ready || !status.TunnelReady || status.DNSReady {
		t.Fatalf("split-DNS health status = %#v", status)
	}
}

func TestReconcileRetainsLastCompletedDNSObservationWhileProbeIsInFlight(t *testing.T) {
	backend := &recordingBackend{}
	engine := &recordingEngine{observation: provider.EngineObservation{TunnelReady: true, DNSReady: true}}
	service := &Service{Config: testConfig(), Backend: backend, Engine: engine, DNSProber: recordingDNSProber{}}
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	service.DNSProber = blockingDNSProber{started: started, release: release, err: errors.New("external DNS unavailable")}
	done := make(chan error, 1)
	go func() { done <- service.Reconcile(context.Background()) }()
	<-started

	if status := service.HealthStatus(); !status.Ready || !status.TunnelReady || !status.DNSReady {
		t.Fatalf("in-flight probe replaced the last completed observation: %#v", status)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("completed DNS failure was hidden")
	}
	if status := service.HealthStatus(); status.Ready || !status.TunnelReady || status.DNSReady {
		t.Fatalf("completed DNS failure did not withdraw readiness: %#v", status)
	}
}

func TestReconcileErrorReportingOnlyReportsTransitions(t *testing.T) {
	var reported []string
	service := &Service{ReconcileErrorHook: func(err error) { reported = append(reported, err.Error()) }}
	previous := service.reportReconcileError(errors.New("tunnel down"), "")
	previous = service.reportReconcileError(errors.New("tunnel down"), previous)
	previous = service.reportReconcileError(nil, previous)
	_ = service.reportReconcileError(errors.New("tunnel down"), previous)
	if len(reported) != 2 || reported[0] != "tunnel down" || reported[1] != "tunnel down" {
		t.Fatalf("reported transitions = %#v", reported)
	}
}

func TestReconcileRecoveryReportsPreviousFailureAndDuration(t *testing.T) {
	var previous string
	var unavailableFor time.Duration
	service := &Service{ReconcileRecoveryHook: func(value string, duration time.Duration) {
		previous = value
		unavailableFor = duration
	}}
	service.reportReconcileRecovery("dns unavailable", time.Now().Add(-20*time.Millisecond))
	if previous != "dns unavailable" || unavailableFor < 20*time.Millisecond {
		t.Fatalf("recovery transition = %q after %s", previous, unavailableFor)
	}
}

func TestDNSListenerUsesDedicatedOverlayPort(t *testing.T) {
	config := testConfig()
	if got, want := dnsListenAddress(config), "100.96.0.1:1053"; got != want {
		t.Fatalf("DNS listen address = %q, want %q", got, want)
	}
	if got, want := config.DNSUpstream.String(), "127.0.0.1:53"; got != want {
		t.Fatalf("DNS upstream = %q, want %q", got, want)
	}
}

type recordingBackend struct{ health []bool }

func (*recordingBackend) EnsureOverlay(context.Context, Config) error { return nil }
func (backend *recordingBackend) ReplaceRules(_ context.Context, _ Config, healthy bool) error {
	backend.health = append(backend.health, healthy)
	return nil
}

type recordingEngine struct {
	observation provider.EngineObservation
	err         error
}

type recordingDNSProber struct{ err error }

func (prober recordingDNSProber) Probe(context.Context) error { return prober.err }

type blockingDNSProber struct {
	started chan<- struct{}
	release <-chan struct{}
	err     error
}

func (prober blockingDNSProber) Probe(context.Context) error {
	close(prober.started)
	<-prober.release
	return prober.err
}

func (engine *recordingEngine) Observe(context.Context) (provider.EngineObservation, error) {
	return engine.observation, engine.err
}
func testConfig() Config {
	return Config{GatewayUID: "uid", OverlayCIDR: netip.MustParsePrefix("100.96.0.0/24"), GatewayAddress: netip.MustParseAddr("100.96.0.1"), OverlayInterface: "waycloak0", UnderlayInterface: "eth0", TunnelInterface: "tun0", VXLANPort: 4789, VNI: 7999, MTU: 1320, HealthPort: 18080, DNSUpstream: netip.MustParseAddrPort("127.0.0.1:53"), ClusterDNSUpstream: netip.MustParseAddrPort("10.43.0.10:53"), ClusterDomain: "cluster.local"}
}
