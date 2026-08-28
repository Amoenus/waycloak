// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

func TestPortForwardCapabilityForConfig(t *testing.T) {
	name, err := PortForwardCapabilityForConfig(map[string]string{"VPN_SERVICE_PROVIDER": " protonvpn ", "VPN_TYPE": "OPENVPN"})
	if err != nil || name != PortForwardCapabilityNative {
		t.Fatalf("select native capability: name=%q err=%v", name, err)
	}
	for _, config := range []map[string]string{{"VPN_SERVICE_PROVIDER": "private internet access", "VPN_TYPE": "openvpn"}, {"VPN_SERVICE_PROVIDER": "protonvpn", "VPN_TYPE": "wireguard"}, {"VPN_SERVICE_PROVIDER": "protonvpn"}} {
		if name, err := PortForwardCapabilityForConfig(config); err == nil || name != "" {
			t.Fatalf("unsupported native config selected capability %q: %v", name, err)
		}
	}
}

func TestNativePortForwardObservesAuthenticatedEngineMapping(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	keyFile := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(keyFile, []byte("test-control-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet || request.Header.Get("X-API-Key") != "test-control-identity" {
			t.Fatalf("unauthenticated control request: %s %#v", request.Method, request.Header)
		}
		switch request.URL.Path {
		case "/v1/portforward":
			_, _ = response.Write([]byte(`{"port":42000,"ports":[42000]}`))
		case "/v1/publicip/ip":
			_, _ = response.Write([]byte(`{"public_ip":"203.0.113.10"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	capability, err := NewPortForwardCapability(PortForwardOptions{CapabilityName: PortForwardCapabilityNative, ControlURL: server.URL, APIKeyFile: keyFile, Client: server.Client(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	request := provider.PortForwardLeaseRequest{Identity: "lease", InternalPort: 40001, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}}
	observation, err := capability.EnsureLease(context.Background(), request)
	if err != nil || requests != 2 || observation.PublicPort != 42000 || observation.PublicAddress.String() != "203.0.113.10" ||
		!observation.IssuedAt.Equal(now) || !observation.RenewAfter.Equal(now.Add(observationRefresh)) || !observation.ExpiresAt.Equal(now.Add(observationValidity)) {
		t.Fatalf("native observation=%#v requests=%d err=%v", observation, requests, err)
	}
	capabilities, err := capability.ObserveCapabilities(context.Background())
	if err != nil || capabilities.MaxLeases != 1 || !capabilities.SharedPort || capabilities.SupportsRequestedPort || len(capabilities.Protocols) != 2 {
		t.Fatalf("native capabilities=%#v err=%v", capabilities, err)
	}
}

func TestNativePortForwardFailsClosedOnMissingMappingAndControlFailure(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(keyFile, []byte("test-control-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = response.Write([]byte(`{"ports":[]}`))
		}
	}))
	defer server.Close()
	capability, err := NewPortForwardCapability(PortForwardOptions{CapabilityName: PortForwardCapabilityNative, ControlURL: server.URL, APIKeyFile: keyFile, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	request := provider.PortForwardLeaseRequest{Identity: "lease", InternalPort: 40001, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}}
	if _, err := capability.EnsureLease(context.Background(), request); err == nil || !strings.Contains(err.Error(), "no exact shared") {
		t.Fatalf("missing mapping error=%v", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := capability.EnsureLease(context.Background(), request); err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("control failure error=%v", err)
	}
}

func TestNewNativePortForwardRejectsNonLoopbackAndMissingIdentity(t *testing.T) {
	for _, options := range []PortForwardOptions{{CapabilityName: "gluetun.waycloak.io/unknown", APIKeyFile: "key"}, {CapabilityName: PortForwardCapabilityNative}, {CapabilityName: PortForwardCapabilityNative, APIKeyFile: "key", ControlURL: "http://gluetun:8000"}, {CapabilityName: PortForwardCapabilityNative, APIKeyFile: "key", ControlURL: "http://127.0.0.1:8000/extra"}} {
		if _, err := NewPortForwardCapability(options); err == nil {
			t.Fatalf("accepted unsafe options %#v", options)
		}
	}
}

func TestNativeProtocolQualificationAcceptsNarrowAndSharedMappings(t *testing.T) {
	for _, protocols := range [][]provider.PortForwardProtocol{{provider.ProtocolTCP}, {provider.ProtocolUDP}, {provider.ProtocolUDP, provider.ProtocolTCP}} {
		if !supportedNativeProtocols(protocols) {
			t.Fatalf("rejected supported protocols %#v", protocols)
		}
	}
	for _, protocols := range [][]provider.PortForwardProtocol{nil, {provider.ProtocolTCP, provider.ProtocolTCP}, {"SCTP"}} {
		if supportedNativeProtocols(protocols) {
			t.Fatalf("accepted unsupported protocols %#v", protocols)
		}
	}
}
