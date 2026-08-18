// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrentadapter

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	wayportforward "github.com/Amoenus/waycloak/internal/portforward"
)

func TestHandlerAcknowledgesExactGenerationAndWithdrawsToBackendPort(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	application := &fakeConfigurer{}
	handler := &Handler{Application: application, Namespace: "apps", Name: "qbittorrent", Image: "registry.invalid/qbit@sha256:aaaaaaaa",
		PodUID: "adapter-pod", Now: func() time.Time { return now }}
	record := validRecord(now)
	response := serveJSON(t, handler, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record)
	if response.Code != http.StatusOK {
		t.Fatalf("delivery response = %d: %s", response.Code, response.Body.String())
	}
	var acknowledgement wayportforward.AdapterAcknowledgement
	if err := json.Unmarshal(response.Body.Bytes(), &acknowledgement); err != nil || acknowledgement.LeaseUID != record.LeaseUID || acknowledgement.ExpiresAt != record.ExpiresAt {
		t.Fatalf("delivery acknowledgement = %#v, %v", acknowledgement, err)
	}
	if len(application.calls) != 1 || application.calls[0] != (configureCall{record.ApplicationAddress, record.TargetPort, true}) {
		t.Fatalf("application delivery calls = %#v", application.calls)
	}

	response = serveJSON(t, handler, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record)
	if response.Code != http.StatusOK || len(application.calls) != 1 {
		t.Fatalf("idempotent delivery response = %d calls=%#v", response.Code, application.calls)
	}

	withdrawal := wayportforward.AdapterWithdrawalIntent{APIVersion: wayportforward.AdapterAPIVersion, LeaseNamespace: record.LeaseNamespace,
		LeaseUID: record.LeaseUID, HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID}
	response = serveJSON(t, handler, http.MethodPost, adapterPathPrefix+string(record.LeaseUID)+"/withdraw", withdrawal)
	if response.Code != http.StatusOK || len(application.calls) != 2 || application.calls[1] != (configureCall{record.ApplicationAddress, record.BackendPort, true}) {
		t.Fatalf("withdrawal response = %d calls=%#v body=%s", response.Code, application.calls, response.Body.String())
	}
	var withdrawn wayportforward.AdapterWithdrawalAcknowledgement
	if err := json.Unmarshal(response.Body.Bytes(), &withdrawn); err != nil || !withdrawn.Withdrawn || withdrawn.PodUID != record.PodUID {
		t.Fatalf("withdrawal acknowledgement = %#v, %v", withdrawn, err)
	}
}

func TestHandlerRejectsWrongPodGenerationAndNonProviderAssignedPort(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	handler := &Handler{Application: &fakeConfigurer{}, Namespace: "apps", Name: "qbittorrent", Image: "digest", PodUID: "adapter-pod", Now: func() time.Time { return now }}
	record := validRecord(now)
	if response := serveJSON(t, handler, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record); response.Code != http.StatusOK {
		t.Fatalf("initial delivery = %d", response.Code)
	}
	record.PodUID = "other-pod"
	if response := serveJSON(t, handler, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record); response.Code != http.StatusConflict {
		t.Fatalf("same-generation Pod replacement = %d", response.Code)
	}
	record = validRecord(now)
	record.LeaseUID = "other-lease"
	record.TargetPort = record.BackendPort
	if response := serveJSON(t, handler, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record); response.Code != http.StatusBadRequest {
		t.Fatalf("non-provider-assigned qBittorrent port = %d", response.Code)
	}
}

func TestHandlerHealthBindsExactAdapterPodAndImage(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	handler := &Handler{Application: &fakeConfigurer{}, Namespace: "apps", Name: "qbittorrent", Image: "digest", PodUID: "adapter-pod", Now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodGet, "/networking.waycloak.io/adapter/v1/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var health wayportforward.AdapterHealthObservation
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &health) != nil || health.PodUID != "adapter-pod" || health.Image != "digest" || !health.Ready {
		t.Fatalf("adapter health = %d %#v", response.Code, health)
	}
}

func TestHandlerDurablyRecoversAndRevalidatesBeforeAcknowledging(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	stateFile := filepath.Join(t.TempDir(), "adapter-state.json")
	record := validRecord(now)
	firstApplication := &fakeConfigurer{}
	first := &Handler{Application: firstApplication, Namespace: "apps", Name: "qbittorrent", Image: "digest", PodUID: "adapter-pod",
		StateFile: stateFile, Now: func() time.Time { return now }}
	if response := serveJSON(t, first, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record); response.Code != http.StatusOK {
		t.Fatalf("persisted delivery = %d: %s", response.Code, response.Body.String())
	}

	restartedApplication := &fakeConfigurer{}
	restarted := &Handler{Application: restartedApplication, Namespace: "apps", Name: "qbittorrent", Image: "digest", PodUID: "replacement-adapter-pod",
		StateFile: stateFile, Now: func() time.Time { return now }}
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	if response := serveJSON(t, restarted, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record); response.Code != http.StatusOK {
		t.Fatalf("restart revalidation = %d: %s", response.Code, response.Body.String())
	}
	if len(restartedApplication.calls) != 1 || !restartedApplication.calls[0].reannounce {
		t.Fatalf("restart did not revalidate and reannounce: %#v", restartedApplication.calls)
	}
	withdrawal := wayportforward.AdapterWithdrawalIntent{APIVersion: wayportforward.AdapterAPIVersion, LeaseNamespace: record.LeaseNamespace,
		LeaseUID: record.LeaseUID, HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID}
	if response := serveJSON(t, restarted, http.MethodPost, adapterPathPrefix+string(record.LeaseUID)+"/withdraw", withdrawal); response.Code != http.StatusOK {
		t.Fatalf("persisted withdrawal = %d: %s", response.Code, response.Body.String())
	}
}

func TestHandlerPersistsIntentBeforeMutationAndWithdrawsFailedDelivery(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	stateFile := filepath.Join(t.TempDir(), "adapter-state.json")
	record := validRecord(now)
	handler := &Handler{Namespace: "apps", Name: "qbittorrent", Image: "digest", PodUID: "adapter-pod",
		StateFile: stateFile, Now: func() time.Time { return now }}
	application := &fakeConfigurer{err: errors.New("application unavailable")}
	application.before = func() {
		contents, err := os.ReadFile(stateFile)
		if err != nil {
			t.Fatalf("delivery intent was not durable before mutation: %v", err)
		}
		states := map[wayv1.ObjectUID]adapterState{}
		if err := json.Unmarshal(contents, &states); err != nil || states[record.LeaseUID].Record.PodUID != record.PodUID {
			t.Fatalf("durable delivery intent = %#v, %v", states, err)
		}
	}
	handler.Application = application
	if response := serveJSON(t, handler, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record); response.Code != http.StatusConflict {
		t.Fatalf("failed application mutation = %d: %s", response.Code, response.Body.String())
	}
	if state, exists := handler.states[record.LeaseUID]; !exists || state.verified {
		t.Fatalf("failed delivery state = %#v, exists=%t", state, exists)
	}

	application.err = nil
	application.before = nil
	withdrawal := wayportforward.AdapterWithdrawalIntent{APIVersion: wayportforward.AdapterAPIVersion, LeaseNamespace: record.LeaseNamespace,
		LeaseUID: record.LeaseUID, HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID}
	if response := serveJSON(t, handler, http.MethodPost, adapterPathPrefix+string(record.LeaseUID)+"/withdraw", withdrawal); response.Code != http.StatusOK {
		t.Fatalf("failed delivery withdrawal = %d: %s", response.Code, response.Body.String())
	}
	if got := application.calls[len(application.calls)-1]; got.port != record.BackendPort {
		t.Fatalf("withdrawal configured port %d, want %d", got.port, record.BackendPort)
	}
}

func TestHandlerAcknowledgesWithdrawalWithoutTrackedMutation(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	application := &fakeConfigurer{}
	handler := &Handler{Application: application, Namespace: "apps", Name: "qbittorrent", Image: "digest", PodUID: "adapter-pod", Now: func() time.Time { return now }}
	record := validRecord(now)
	withdrawal := wayportforward.AdapterWithdrawalIntent{APIVersion: wayportforward.AdapterAPIVersion, LeaseNamespace: record.LeaseNamespace,
		LeaseUID: record.LeaseUID, HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID}
	response := serveJSON(t, handler, http.MethodPost, adapterPathPrefix+string(record.LeaseUID)+"/withdraw", withdrawal)
	var acknowledgement wayportforward.AdapterWithdrawalAcknowledgement
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &acknowledgement) != nil || !acknowledgement.Withdrawn {
		t.Fatalf("absent-state withdrawal = %d %#v: %s", response.Code, acknowledgement, response.Body.String())
	}
	if len(application.calls) != 0 {
		t.Fatalf("absent-state withdrawal mutated application: %#v", application.calls)
	}
}

func TestHandlerClearsExactRetiredPodWhenBackendRestoreIsImpossible(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	application := &fakeConfigurer{}
	handler := &Handler{Application: application, Namespace: "apps", Name: "qbittorrent", Image: "digest", PodUID: "adapter-pod", Now: func() time.Time { return now }}
	record := validRecord(now)
	if response := serveJSON(t, handler, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record); response.Code != http.StatusOK {
		t.Fatalf("initial delivery = %d: %s", response.Code, response.Body.String())
	}
	application.err = errors.New("retired application endpoint is unreachable")
	withdrawal := wayportforward.AdapterWithdrawalIntent{APIVersion: wayportforward.AdapterAPIVersion, LeaseNamespace: record.LeaseNamespace,
		LeaseUID: record.LeaseUID, HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID, ApplicationEndpointRetired: true}
	response := serveJSON(t, handler, http.MethodPost, adapterPathPrefix+string(record.LeaseUID)+"/withdraw", withdrawal)
	if response.Code != http.StatusOK {
		t.Fatalf("retired endpoint withdrawal = %d: %s", response.Code, response.Body.String())
	}
	if _, exists := handler.states[record.LeaseUID]; exists {
		t.Fatal("retired endpoint state was not cleared")
	}

	application.err = nil
	if response := serveJSON(t, handler, http.MethodPut, adapterPathPrefix+string(record.LeaseUID), record); response.Code != http.StatusOK {
		t.Fatalf("replacement setup = %d: %s", response.Code, response.Body.String())
	}
	application.err = errors.New("current application endpoint is unreachable")
	withdrawal.ApplicationEndpointRetired = false
	response = serveJSON(t, handler, http.MethodPost, adapterPathPrefix+string(record.LeaseUID)+"/withdraw", withdrawal)
	if response.Code != http.StatusConflict {
		t.Fatalf("current endpoint withdrawal = %d, want conflict", response.Code)
	}
	if _, exists := handler.states[record.LeaseUID]; !exists {
		t.Fatal("current endpoint state was cleared without restoration")
	}
}

func TestHandlerSeparatesControllerHealthFromGatewayRuntimeAuthority(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	controllerIdentity := "spiffe://waycloak.io/replacement-controller"
	runtimeIdentity := "spiffe://waycloak.io/gateway-runtime"
	handler := &Handler{Application: &fakeConfigurer{}, Namespace: "apps", Name: "qbittorrent", Image: "digest", PodUID: "adapter-pod",
		HealthIdentity: controllerIdentity, RuntimeIdentity: runtimeIdentity, Now: func() time.Time { return now }}
	health := httptest.NewRequest(http.MethodGet, "/networking.waycloak.io/adapter/v1/healthz", nil)
	health.TLS = peerTLSState(controllerIdentity)
	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, health)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("controller health authority = %d", healthResponse.Code)
	}

	record := validRecord(now)
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	delivery := httptest.NewRequest(http.MethodPut, adapterPathPrefix+string(record.LeaseUID), bytes.NewReader(body))
	delivery.Header.Set("Content-Type", "application/json")
	delivery.TLS = peerTLSState(controllerIdentity)
	deliveryResponse := httptest.NewRecorder()
	handler.ServeHTTP(deliveryResponse, delivery)
	if deliveryResponse.Code != http.StatusForbidden {
		t.Fatalf("controller gained lease delivery authority: %d", deliveryResponse.Code)
	}
}

func peerTLSState(identity string) *tls.ConnectionState {
	uri, _ := url.Parse(identity)
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{uri}}}}
}

type configureCall struct {
	address    netip.Addr
	port       uint16
	reannounce bool
}

type fakeConfigurer struct {
	calls  []configureCall
	before func()
	err    error
}

func (f *fakeConfigurer) Configure(_ context.Context, address netip.Addr, port uint16, reannounce bool) error {
	if f.before != nil {
		f.before()
	}
	f.calls = append(f.calls, configureCall{address, port, reannounce})
	return f.err
}

func validRecord(now time.Time) wayportforward.AdapterLeaseRecord {
	return wayportforward.AdapterLeaseRecord{APIVersion: wayportforward.AdapterAPIVersion, LeaseNamespace: "apps", LeaseUID: "lease-uid",
		HandoffGeneration: 1, PodUID: "application-pod", PublicAddress: netip.MustParseAddr("198.51.100.10"), PublicPort: 42000,
		ExpiresAt: now.Add(time.Minute), TargetPort: 42000, ApplicationAddress: netip.MustParseAddr("10.42.0.10"), BackendPort: 6881,
		Protocols: []wayv1.TransportProtocol{wayv1.ProtocolTCP, wayv1.ProtocolUDP}}
}

func serveJSON(t *testing.T, handler http.Handler, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
