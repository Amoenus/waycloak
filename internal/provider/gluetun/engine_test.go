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

func TestObserveRequiresTunnelDNSAndPublicIP(t *testing.T) {
	dnsRunning := true
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
			_, _ = response.Write([]byte(`{"public_ip":"203.0.113.10"}`))
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

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
