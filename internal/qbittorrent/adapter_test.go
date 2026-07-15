// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrent

import (
	"encoding/json"
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
			_ = json.NewEncoder(response).Encode(map[string]int{"listen_port": listenPort})
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
			document := delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicPort: uint16(applicationPort), TargetPort: 6881, ApplicationPort: uint16(applicationPort), ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: 4, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
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
	if err := adapter.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if listenPort != applicationPort || acknowledgement.Generation != 4 || acknowledgement.ApplicationPort != uint16(applicationPort) {
		t.Fatalf("listenPort=%d acknowledgement=%#v", listenPort, acknowledgement)
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
			_ = json.NewEncoder(response).Encode(map[string]int{"listen_port": unavailablePort})
		case "/v1/port-forward/leases":
			document := delivery.Document{APIVersion: delivery.APIVersion, PodUID: "pod-uid", Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicPort: uint16(unavailablePort), TargetPort: 6881, ApplicationPort: uint16(unavailablePort), ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: 4, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
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
	if err := adapter.Reconcile(t.Context()); err == nil {
		t.Fatal("adapter acknowledged an unavailable qBitTorrent listener")
	}
	if acknowledged {
		t.Fatal("adapter posted an acknowledgement for an unavailable qBitTorrent listener")
	}
}

func TestClientRejectsNonLoopbackEndpoint(t *testing.T) {
	client := &Client{BaseURL: (&url.URL{Scheme: "http", Host: "192.0.2.1:" + strconv.Itoa(8080)}).String(), APIKey: "test-key"}
	if err := client.Validate(); err == nil {
		t.Fatal("non-loopback qBitTorrent endpoint was accepted")
	}
}
