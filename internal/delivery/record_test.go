// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/contract"
)

func TestStoreAcknowledgesOnlyCurrentExactRecord(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	document := testDocument(now)
	serialized, err := Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, contract.PortForwardLeasesKey), []byte(serialized), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &Store{Now: func() time.Time { return now }}
	if err := store.Refresh(directory); err != nil {
		t.Fatal(err)
	}
	observation, err := store.Observe("lease-uid")
	if err != nil || !observation.Ready || observation.PodUID != "pod-uid" || observation.Generation != 4 {
		t.Fatalf("observation=%#v error=%v", observation, err)
	}
	now = now.Add(61 * time.Second)
	if _, err := store.Observe("lease-uid"); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("expired observation error = %v", err)
	}
}

func TestDocumentRejectsNondeterministicOrInvalidRecords(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	document := testDocument(now)
	document.Leases[0].Protocols = []string{"UDP", "TCP"}
	if err := document.Validate(now); err == nil {
		t.Fatal("unsorted protocols were accepted")
	}
	document = testDocument(now)
	document.Leases[0].ExpiresAt = now
	if err := document.Validate(now); err == nil {
		t.Fatal("expired record was accepted")
	}
}

func TestHTTPObserverSelectsExactIdentityWithoutLeakingBodies(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/port-forward/deliveries/lease-uid" {
			_, _ = response.Write([]byte(`{"apiVersion":"networking.waycloak.io/v1alpha1","identity":"lease-uid","podUID":"pod-uid","generation":4,"expiresAt":"2026-07-13T12:01:00Z","ready":true}`))
			return
		}
		http.Error(response, "credential-value", http.StatusUnauthorized)
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	observer := &HTTPObserver{Client: server.Client(), Port: port}
	observation, err := observer.ObserveDelivery(context.Background(), host, "lease-uid")
	if err != nil || !observation.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("observation=%#v error=%v", observation, err)
	}
	_, err = observer.ObserveDelivery(context.Background(), host, "other")
	if err == nil || err.Error() != "pod delivery observation returned HTTP 401" {
		t.Fatalf("sanitized error = %v", err)
	}
}

func testDocument(now time.Time) Document {
	return Document{APIVersion: APIVersion, PodUID: "pod-uid", Leases: []Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicPort: 42000, TargetPort: 6881, Protocols: []string{"TCP", "UDP"}, Generation: 4, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
}
