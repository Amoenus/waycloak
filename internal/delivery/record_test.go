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

func TestProviderAssignedDeliveryRequiresExactAppliedAcknowledgement(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	document := testDocument(now)
	document.Leases[0].ApplicationPortMode = ApplicationPortModeProviderAssigned
	document.Leases[0].ApplicationPort = document.Leases[0].PublicPort
	serialized, err := Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, contract.PortForwardLeasesKey)
	if err := os.WriteFile(path, []byte(serialized), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &Store{Now: func() time.Time { return now }}
	if err := store.Refresh(directory); err != nil {
		t.Fatal(err)
	}
	observation, err := store.Observe("lease-uid")
	if err != nil || observation.Ready {
		t.Fatalf("unacknowledged observation=%#v error=%v", observation, err)
	}
	if err := store.Acknowledge("lease-uid", ApplicationAcknowledgement{APIVersion: AcknowledgementAPIVersion, PodUID: "pod-uid", LeaseIdentity: "lease-uid", Generation: 3, ApplicationPort: 42000}); !errors.Is(err, ErrAcknowledgementMismatch) {
		t.Fatalf("stale acknowledgement error = %v", err)
	}
	if err := store.Acknowledge("unknown", ApplicationAcknowledgement{APIVersion: AcknowledgementAPIVersion, PodUID: "pod-uid", LeaseIdentity: "unknown", Generation: 4, ApplicationPort: 42000}); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("unknown acknowledgement error = %v", err)
	}
	acknowledgement := ApplicationAcknowledgement{APIVersion: AcknowledgementAPIVersion, PodUID: "pod-uid", LeaseIdentity: "lease-uid", Generation: 4, ApplicationPort: 42000}
	if err := store.Acknowledge("lease-uid", acknowledgement); err != nil {
		t.Fatal(err)
	}
	redirects := store.RequestedRedirects()
	if len(redirects) != 1 || redirects[0].TargetPort != 6881 || redirects[0].ApplicationPort != 42000 {
		t.Fatalf("redirects = %#v", redirects)
	}
	if observation, _ := store.Observe("lease-uid"); observation.Ready {
		t.Fatal("request acknowledgement became ready before kernel application")
	}
	store.MarkApplied(redirects)
	observation, err = store.Observe("lease-uid")
	if err != nil || !observation.Ready || observation.AppliedPort != 42000 {
		t.Fatalf("applied observation=%#v error=%v", observation, err)
	}
	document.Leases[0].Generation = 5
	document.Leases[0].PublicPort = 42001
	document.Leases[0].ApplicationPort = 42001
	serialized, _ = Marshal(document)
	if err := os.WriteFile(path, []byte(serialized), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Refresh(directory); err != nil {
		t.Fatal(err)
	}
	observation, _ = store.Observe("lease-uid")
	if observation.Ready || len(store.RequestedRedirects()) != 0 {
		t.Fatal("rotation retained a stale application acknowledgement")
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
	return Document{APIVersion: APIVersion, PodUID: "pod-uid", Leases: []Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicAddress: "203.0.113.10", PublicPort: 42000, TargetPort: 6881, Protocols: []string{"TCP", "UDP"}, Generation: 4, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(time.Minute)}}}
}
