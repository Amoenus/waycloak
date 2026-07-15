// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	"github.com/Amoenus/waycloak/internal/provider"
)

type handlerEngine struct{}

func (*handlerEngine) Observe(context.Context) (provider.EngineObservation, error) {
	return provider.EngineObservation{}, nil
}

type handlerSource struct{ desired waygateway.DesiredState }

func (source handlerSource) Load() (waygateway.DesiredState, error) { return source.desired, nil }

func TestEngineForRejectsUnsupportedTypes(t *testing.T) {
	if _, err := engineFor("FakeVPN", "", ""); err == nil {
		t.Fatal("unsupported engine was accepted")
	}
	if _, err := engineFor("Gluetun", "http://127.0.0.1:9999/", "http://127.0.0.1:8000"); err != nil {
		t.Fatal(err)
	}
}

func TestPortForwardDriverForIsExplicit(t *testing.T) {
	if driver, err := portForwardDriverFor("", waygateway.TunnelInterface, ""); err != nil || driver != nil {
		t.Fatalf("disabled driver=%#v error=%v", driver, err)
	}
	if _, err := portForwardDriverFor("ProtonNatPmp", waygateway.TunnelInterface, "10.2.0.1:5351"); err != nil {
		t.Fatal(err)
	}
	if _, err := portForwardDriverFor("unknown", waygateway.TunnelInterface, ""); err == nil {
		t.Fatal("unknown provider driver was accepted")
	}
}

func TestManagerHandlerPublishesReadOnlyLeaseObservations(t *testing.T) {
	driver := &handlerPortForwardDriver{}
	portForwarding := &waygateway.PortForwardManager{Driver: driver}
	intent := waygateway.PortForwardLeaseIntent{Identity: "lease-uid", InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}}
	if err := portForwarding.Reconcile(context.Background(), []waygateway.PortForwardLeaseIntent{intent}); err != nil {
		t.Fatal(err)
	}
	manager := &waygateway.HealthManager{PortForwarding: portForwarding}
	request := httptest.NewRequest(http.MethodGet, "/v1/port-forward/leases/lease-uid", nil)
	response := httptest.NewRecorder()
	managerHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"identity":"lease-uid"`) || !strings.Contains(response.Body.String(), `"publicPort":42000`) {
		t.Fatalf("response code=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/port-forward/leases/lease-uid", nil)
	response = httptest.NewRecorder()
	managerHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST response code = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/v1/port-forward/leases/", nil)
	response = httptest.NewRecorder()
	managerHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("lease enumeration response code = %d", response.Code)
	}
}

func TestManagerHandlerPublishesAppliedMembershipGeneration(t *testing.T) {
	members := []waygateway.Member{{ID: "member", OverlayAddress: "172.30.99.2", UnderlayIP: "10.42.0.2"}}
	generation := waygateway.MembershipGeneration(members)
	manager := &waygateway.HealthManager{Engine: &handlerEngine{}, Source: handlerSource{desired: waygateway.DesiredState{MembershipGeneration: generation, Members: members}}}
	manager.Reconcile(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/v1/gateway/observation", nil)
	response := httptest.NewRecorder()
	managerHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), generation) {
		t.Fatalf("response code=%d body=%s", response.Code, response.Body.String())
	}
}

type handlerPortForwardDriver struct{}

func (*handlerPortForwardDriver) ObserveCapabilities(context.Context) (provider.PortForwardCapabilities, error) {
	return provider.PortForwardCapabilities{}, nil
}

func (*handlerPortForwardDriver) EnsureLease(context.Context, provider.PortForwardLeaseRequest) (provider.PortForwardLeaseObservation, error) {
	now := time.Now().UTC()
	return provider.PortForwardLeaseObservation{PublicPort: 42000, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(60 * time.Second)}, nil
}

func (*handlerPortForwardDriver) ReleaseLease(context.Context, provider.PortForwardLeaseRequest) error {
	return nil
}

func TestRenderEngineFirewallCommand(t *testing.T) {
	directory := t.TempDir()
	basePath := filepath.Join(directory, "base.txt")
	resolvPath := filepath.Join(directory, "resolv.conf")
	outputPath := filepath.Join(directory, "post-rules.txt")
	resolverOutputPath := filepath.Join(directory, "captured-resolv.conf")
	if err := os.WriteFile(basePath, []byte("iptables --policy FORWARD ACCEPT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolvPath, []byte("nameserver 10.43.0.10\nsearch apps.svc.cluster.local svc.cluster.local cluster.local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"render-engine-firewall", "--base-path=" + basePath, "--resolv-conf=" + resolvPath, "--output=" + outputPath, "--resolver-output=" + resolverOutputPath}); err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "--destination 10.43.0.10/32 --protocol udp --destination-port 53 --jump ACCEPT") {
		t.Fatalf("rendered engine firewall = %s", rendered)
	}
	captured, err := os.ReadFile(resolverOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(captured) != "nameserver 10.43.0.10\nsearch apps.svc.cluster.local. svc.cluster.local. cluster.local.\n" {
		t.Fatalf("captured resolver configuration = %q", captured)
	}
}
