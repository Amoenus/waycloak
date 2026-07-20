// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestObserveRequiresTunnelAndDNSWhilePublicIPIsBestEffort(t *testing.T) {
	dnsRunning := true
	publicIPResponse := `{"public_ip":"203.0.113.10"}`
	publicIPStatus := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			response.WriteHeader(http.StatusOK)
		case "/v1/dns/status":
			if dnsRunning {
				_, _ = response.Write([]byte(`{"status":"running"}`))
			} else {
				_, _ = response.Write([]byte(`{"status":"stopped"}`))
			}
		case "/v1/publicip/ip":
			response.WriteHeader(publicIPStatus)
			_, _ = response.Write([]byte(publicIPResponse))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	engine := &Engine{HealthURL: server.URL + "/health", ControlURL: server.URL, Client: server.Client()}
	observation, err := engine.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !observation.TunnelReady || !observation.DNSReady || observation.PublicIP.String() != "203.0.113.10" {
		t.Fatalf("observation = %#v", observation)
	}
	publicIPResponse = `{"public_ip":""}`
	observation, err = engine.Observe(context.Background())
	if err != nil || !observation.TunnelReady || !observation.DNSReady || observation.PublicIP.IsValid() {
		t.Fatalf("missing public IP observation=%#v error=%v", observation, err)
	}
	publicIPResponse = `{"public_ip":"not-an-ip"}`
	observation, err = engine.Observe(context.Background())
	if err != nil || !observation.TunnelReady || !observation.DNSReady || observation.PublicIP.IsValid() {
		t.Fatalf("malformed public IP observation=%#v error=%v", observation, err)
	}
	publicIPStatus = http.StatusInternalServerError
	observation, err = engine.Observe(context.Background())
	if err != nil || !observation.TunnelReady || !observation.DNSReady || observation.PublicIP.IsValid() {
		t.Fatalf("failed public IP lookup observation=%#v error=%v", observation, err)
	}
	dnsRunning = false
	observation, err = engine.Observe(context.Background())
	if !errors.Is(err, ErrDNSUnhealthy) || !observation.TunnelReady || observation.DNSReady {
		t.Fatalf("DNS failure observation=%#v error=%v", observation, err)
	}
}

func TestObserveDoesNotReturnProviderResponseBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte("credential-value-that-must-not-escape"))
	}))
	defer server.Close()
	engine := &Engine{HealthURL: server.URL, ControlURL: server.URL, Client: server.Client()}
	_, err := engine.Observe(context.Background())
	if err == nil || errors.Is(err, ErrDNSUnhealthy) || err.Error() == "credential-value-that-must-not-escape" {
		t.Fatalf("unexpected error = %v", err)
	}
	if contains(err.Error(), "credential-value") {
		t.Fatalf("provider response escaped in error: %v", err)
	}
}

func TestNewEngineDisablesKeepAlives(t *testing.T) {
	engine := New()
	transport, ok := engine.Client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected Engine.Client.Transport to be *http.Transport")
	}
	if !transport.DisableKeepAlives {
		t.Fatalf("expected DisableKeepAlives to be true to prevent loopback health check flaps")
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
