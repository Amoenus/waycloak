// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package bitmagnet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/delivery"
)

func TestAdapterStagesObservesAndAcknowledgesExactGeneration(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	var acknowledgement delivery.ApplicationAcknowledgement
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/port-forward/leases":
			_ = json.NewEncoder(response).Encode(document(now, 4, 64327))
		case "/v1/port-forward/leases/lease-uid/ack":
			_ = json.NewDecoder(request.Body).Decode(&acknowledgement)
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.yml")
	adapter := &Adapter{LeaseEndpoint: server.URL + "/v1/port-forward/leases", LeaseName: "bitmagnet", ConfigPath: configPath, HTTP: server.Client(), Now: func() time.Time { return now }, ListenerReady: func(port uint16) (bool, error) { return port == 64327, nil }}
	revision, err := adapter.Reconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if revision != (Revision{Identity: "lease-uid", Generation: 4, ApplicationPort: 64327, ConfigChanged: true}) {
		t.Fatalf("revision = %#v", revision)
	}
	config, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(config), "port: 64327") {
		t.Fatalf("config = %q err=%v", config, err)
	}
	want := delivery.ApplicationAcknowledgement{APIVersion: delivery.AcknowledgementAPIVersion, PodUID: "pod-uid", LeaseIdentity: "lease-uid", Generation: 4, ApplicationPort: 64327}
	if acknowledgement != want {
		t.Fatalf("acknowledgement = %#v, want %#v", acknowledgement, want)
	}
}

func TestAdapterStagesRotationButDoesNotAcknowledgeBeforeListenerMoves(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	acknowledged := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ack") {
			acknowledged = true
			response.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(response).Encode(document(now, 5, 65000))
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(configPath, []byte("dht_server:\n  port: 64327\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{LeaseEndpoint: server.URL, ConfigPath: configPath, HTTP: server.Client(), Now: func() time.Time { return now }, ListenerReady: func(uint16) (bool, error) { return false, nil }}
	revision, err := adapter.Reconcile(t.Context())
	if err == nil || !revision.ConfigChanged || acknowledged {
		t.Fatalf("revision=%#v err=%v acknowledged=%v", revision, err, acknowledged)
	}
	config, _ := os.ReadFile(configPath)
	if !strings.Contains(string(config), "port: 65000") {
		t.Fatalf("rotated config = %q", config)
	}
}

func TestStageIsIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(document(now, 1, 61234))
	}))
	defer server.Close()
	adapter := &Adapter{LeaseEndpoint: server.URL, ConfigPath: filepath.Join(t.TempDir(), "config.yml"), HTTP: server.Client(), Now: func() time.Time { return now }}
	first, err := adapter.Stage(t.Context())
	if err != nil || !first.ConfigChanged {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := adapter.Stage(t.Context())
	if err != nil || second.ConfigChanged {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

func document(now time.Time, generation int64, port uint16) delivery.Document {
	return delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "bitmagnet", State: "Active", Gateway: "egress/private", PublicAddress: "203.0.113.10", PublicPort: port, TargetPort: 3334, ApplicationPort: port, ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: generation, IssuedAt: now.Add(-time.Second), RenewAfter: now.Add(30 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
}
