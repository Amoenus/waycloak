// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestNewEngineUsesBoundedLoopbackPool(t *testing.T) {
	engine := New()
	transport, ok := engine.Client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected Engine.Client.Transport to be *http.Transport")
	}
	if transport.DisableKeepAlives || transport.Proxy != nil || transport.MaxIdleConnsPerHost != 4 || transport.IdleConnTimeout <= 0 {
		t.Fatalf("unexpected loopback transport = %#v", transport)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestObserveVerifiesTransientTransportFailureWithSecondRequest(t *testing.T) {
	var output bytes.Buffer
	healthCalls := 0
	engine := &Engine{HealthURL: "http://127.0.0.1/health", ControlURL: "http://127.0.0.1", Logger: slog.New(slog.NewJSONHandler(&output, nil)), Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/health" {
			healthCalls++
			if healthCalls == 1 {
				return nil, io.EOF
			}
		}
		body := ""
		switch request.URL.Path {
		case "/v1/dns/status":
			body = `{"status":"running"}`
		case "/v1/publicip/ip":
			body = `{"public_ip":"203.0.113.10"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}

	observation, err := engine.Observe(context.Background())
	if err != nil || healthCalls != 2 || !observation.TunnelReady || !observation.DNSReady {
		t.Fatalf("observation=%#v healthCalls=%d error=%v", observation, healthCalls, err)
	}
	if logs := output.String(); !strings.Contains(logs, `"event":"gluetun_transport_verification"`) || !strings.Contains(logs, `"component":"tunnel-health"`) || !strings.Contains(logs, `"recovered":true`) {
		t.Fatalf("missing structured verification log: %s", logs)
	}
}

func TestObserveDoesNotRetryAuthoritativeHTTPFailure(t *testing.T) {
	healthCalls := 0
	engine := &Engine{HealthURL: "http://127.0.0.1/health", Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		healthCalls++
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("unhealthy")), Header: make(http.Header)}, nil
	})}}

	_, err := engine.Observe(context.Background())
	if !errors.Is(err, ErrTunnelUnhealthy) || healthCalls != 1 {
		t.Fatalf("healthCalls=%d error=%v", healthCalls, err)
	}
}

func TestObserveWithdrawsReadinessAfterSecondTransportFailure(t *testing.T) {
	healthCalls := 0
	engine := &Engine{HealthURL: "http://127.0.0.1/health", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		healthCalls++
		return nil, io.ErrUnexpectedEOF
	})}}

	observation, err := engine.Observe(context.Background())
	if !errors.Is(err, ErrTunnelUnhealthy) || healthCalls != 2 || observation.TunnelReady {
		t.Fatalf("observation=%#v healthCalls=%d error=%v", observation, healthCalls, err)
	}
}

func TestObserveDoesNotRetryTimeout(t *testing.T) {
	healthCalls := 0
	engine := &Engine{HealthURL: "http://127.0.0.1/health", Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		healthCalls++
		return nil, context.DeadlineExceeded
	})}}

	_, err := engine.Observe(context.Background())
	if !errors.Is(err, ErrTunnelUnhealthy) || healthCalls != 1 {
		t.Fatalf("healthCalls=%d error=%v", healthCalls, err)
	}
}

func TestObserveSustainsIntermittentTransportFailures(t *testing.T) {
	healthCalls := 0
	engine := &Engine{HealthURL: "http://127.0.0.1/health", ControlURL: "http://127.0.0.1", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/health" {
			healthCalls++
			if healthCalls%2 == 1 {
				return nil, io.ErrUnexpectedEOF
			}
		}
		body := ""
		if request.URL.Path == "/v1/dns/status" {
			body = `{"status":"running"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}

	for attempt := 0; attempt < 100; attempt++ {
		observation, err := engine.Observe(context.Background())
		if err != nil || !observation.TunnelReady || !observation.DNSReady {
			t.Fatalf("attempt=%d observation=%#v error=%v", attempt, observation, err)
		}
	}
	if healthCalls != 200 {
		t.Fatalf("health calls=%d, want 200", healthCalls)
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
