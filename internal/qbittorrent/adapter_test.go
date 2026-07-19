// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrent

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/delivery"
)

func TestAdapterAppliesAndAcknowledgesExactGeneration(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	listenPort := 6881
	applicationPort := 0
	var acknowledgement delivery.ApplicationAcknowledgement
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v2/app/preferences" {
			if request.Header.Get("Authorization") != "Bearer test-key" {
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(response).Encode(map[string]any{"listen_port": listenPort, "announce_ip": "203.0.113.10"})
			return
		}
		if request.URL.Path == "/api/v2/app/setPreferences" {
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			var preferences map[string]int
			if err := json.Unmarshal([]byte(request.Form.Get("json")), &preferences); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			listenPort = preferences["listen_port"]
			mu.Unlock()
			response.WriteHeader(http.StatusNoContent)
			return
		}
		if request.URL.Path == "/v1/port-forward/leases" {
			document := delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicAddress: "203.0.113.10", PublicPort: uint16(applicationPort), TargetPort: 6881, ApplicationPort: uint16(applicationPort), ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: 4, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
			_ = json.NewEncoder(response).Encode(document)
			return
		}
		if request.URL.Path == "/v1/port-forward/leases/lease-uid/ack" {
			_ = json.NewDecoder(request.Body).Decode(&acknowledgement)
			response.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	applicationPort = server.Listener.Addr().(*net.TCPAddr).Port
	adapter := &Adapter{Client: &Client{BaseURL: server.URL, APIKey: "test-key", HTTP: server.Client()}, LeaseEndpoint: server.URL + "/v1/port-forward/leases", HTTP: server.Client(), Now: func() time.Time { return now }}
	if _, err := adapter.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if listenPort != applicationPort || acknowledgement.APIVersion != delivery.AcknowledgementAPIVersion || acknowledgement.PodUID != "pod-uid" || acknowledgement.LeaseIdentity != "lease-uid" || acknowledgement.Generation != 4 || acknowledgement.ApplicationPort != uint16(applicationPort) {
		t.Fatalf("listenPort=%d acknowledgement=%#v", listenPort, acknowledgement)
	}
}

func TestAdapterBindsQBittorrentToWaycloakAndRestartsEnabledDHT(t *testing.T) {
	now := time.Date(2026, 7, 18, 23, 30, 0, 0, time.UTC)
	applicationPort := 0
	preferences := map[string]any{"listen_port": 6881, "dht": true, "current_network_interface": "", "current_interface_address": "", "announce_ip": ""}
	var updates []map[string]any
	acknowledged := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/app/preferences":
			preferences["listen_port"] = applicationPort
			_ = json.NewEncoder(response).Encode(preferences)
		case "/api/v2/app/setPreferences":
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			var update map[string]any
			if err := json.Unmarshal([]byte(request.Form.Get("json")), &update); err != nil {
				t.Fatal(err)
			}
			updates = append(updates, update)
			for key, value := range update {
				preferences[key] = value
			}
			response.WriteHeader(http.StatusNoContent)
		case "/v1/port-forward/leases":
			_ = json.NewEncoder(response).Encode(delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicAddress: "203.0.113.10", PublicPort: uint16(applicationPort), TargetPort: 6881, ApplicationPort: uint16(applicationPort), ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: 5, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}})
		case "/v1/port-forward/leases/lease-uid/ack":
			acknowledged = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	applicationPort = server.Listener.Addr().(*net.TCPAddr).Port
	adapter := &Adapter{
		Client:        &Client{BaseURL: server.URL, APIKey: "test-key", HTTP: server.Client()},
		LeaseEndpoint: server.URL + "/v1/port-forward/leases",
		HTTP:          server.Client(),
		Now:           func() time.Time { return now },
		NetworkBinding: func() (NetworkBinding, error) {
			return NetworkBinding{InterfaceName: "wc123456789abc", Address: "127.0.0.1"}, nil
		},
	}
	if _, err := adapter.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !acknowledged || len(updates) != 4 {
		t.Fatalf("acknowledged=%t updates=%#v", acknowledged, updates)
	}
	if updates[0]["current_network_interface"] != "wc123456789abc" || updates[0]["current_interface_address"] != "127.0.0.1" || updates[1]["announce_ip"] != "203.0.113.10" || updates[2]["dht"] != false || updates[3]["dht"] != true {
		t.Fatalf("qBitTorrent compatibility updates = %#v", updates)
	}
}

func TestAdapterDoesNotAcknowledgeWhenQbittorrentListenerIsUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unavailablePort := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	acknowledged := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/app/preferences":
			_ = json.NewEncoder(response).Encode(map[string]any{"listen_port": unavailablePort, "announce_ip": "203.0.113.10"})
		case "/v1/port-forward/leases":
			document := delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicAddress: "203.0.113.10", PublicPort: uint16(unavailablePort), TargetPort: 6881, ApplicationPort: uint16(unavailablePort), ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: 4, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
			_ = json.NewEncoder(response).Encode(document)
		case "/v1/port-forward/leases/lease-uid/ack":
			acknowledged = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter := &Adapter{Client: &Client{BaseURL: server.URL, APIKey: "test-key", HTTP: server.Client()}, LeaseEndpoint: server.URL + "/v1/port-forward/leases", HTTP: server.Client(), Now: func() time.Time { return now }}
	if _, err := adapter.Reconcile(t.Context()); err == nil {
		t.Fatal("adapter acknowledged an unavailable qBitTorrent listener")
	} else {
		var reconcileError *ReconcileError
		if !errors.As(err, &reconcileError) || reconcileError.Kind != FailureCritical {
			t.Fatalf("listener failure classification = %#v", reconcileError)
		}
	}
	if acknowledged {
		t.Fatal("adapter posted an acknowledgement for an unavailable qBitTorrent listener")
	}
}

func TestAdapterClassifiesQbittorrentControlAPITimeoutAsTransientForExactRevision(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/port-forward/leases":
			document := delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicAddress: "203.0.113.10", PublicPort: 64327, TargetPort: 6881, ApplicationPort: 64327, ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: 17, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
			_ = json.NewEncoder(response).Encode(document)
		case "/api/v2/app/preferences":
			time.Sleep(50 * time.Millisecond)
			_ = json.NewEncoder(response).Encode(map[string]int{"listen_port": 64327})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter := &Adapter{
		Client:        &Client{BaseURL: server.URL, APIKey: "test-key", HTTP: &http.Client{Timeout: 10 * time.Millisecond}},
		LeaseEndpoint: server.URL + "/v1/port-forward/leases",
		HTTP:          server.Client(),
		Now:           func() time.Time { return now },
	}
	revision, err := adapter.Reconcile(t.Context())
	if err == nil {
		t.Fatal("qBitTorrent timeout unexpectedly reconciled")
	}
	var reconcileError *ReconcileError
	if !errors.As(err, &reconcileError) || reconcileError.Kind != FailureTransientControlObservation {
		t.Fatalf("control API failure classification = %#v", reconcileError)
	}
	want := LeaseRevision{Identity: "lease-uid", Generation: 17, ApplicationPort: 64327}
	if revision != want || reconcileError.Revision != want {
		t.Fatalf("revision = %#v error revision = %#v, want %#v", revision, reconcileError.Revision, want)
	}
}

func TestAdapterClassifiesQbittorrentControlAPIRejectionAsCritical(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/port-forward/leases":
			document := delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicAddress: "203.0.113.10", PublicPort: 64327, TargetPort: 6881, ApplicationPort: 64327, ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: 17, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
			_ = json.NewEncoder(response).Encode(document)
		case "/api/v2/app/preferences":
			response.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter := &Adapter{Client: &Client{BaseURL: server.URL, APIKey: "test-key", HTTP: server.Client()}, LeaseEndpoint: server.URL + "/v1/port-forward/leases", HTTP: server.Client(), Now: func() time.Time { return now }}
	_, err := adapter.Reconcile(t.Context())
	var reconcileError *ReconcileError
	if !errors.As(err, &reconcileError) || reconcileError.Kind != FailureCritical {
		t.Fatalf("control API rejection classification = %#v", reconcileError)
	}
}

func TestClientRejectsNonLoopbackEndpoint(t *testing.T) {
	client := &Client{BaseURL: (&url.URL{Scheme: "http", Host: "192.0.2.1:" + strconv.Itoa(8080)}).String(), APIKey: "test-key"}
	if err := client.Validate(); err == nil {
		t.Fatal("non-loopback qBitTorrent endpoint was accepted")
	}
}
