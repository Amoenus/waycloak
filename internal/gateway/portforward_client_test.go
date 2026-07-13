// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestHTTPPortForwardObserverSelectsExactIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/port-forward/leases/wanted" && request.URL.Path != "/v1/port-forward/leases/missing" {
			http.NotFound(response, request)
			return
		}
		if request.URL.Path == "/v1/port-forward/leases/missing" {
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write([]byte(`{"apiVersion":"networking.waycloak.io/v1alpha1","lease":{"identity":"wanted","internalPort":2,"protocols":["TCP","UDP"],"publicPort":42000,"ready":true}}`))
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	observer := &HTTPPortForwardObserver{Client: server.Client(), Port: port}
	observation, err := observer.ObserveLease(context.Background(), host, "wanted")
	if err != nil || observation.Identity != "wanted" || observation.PublicPort != 42000 {
		t.Fatalf("observation=%#v error=%v", observation, err)
	}
	_, err = observer.ObserveLease(context.Background(), host, "missing")
	if !errors.Is(err, ErrPortForwardObservationNotFound) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestHTTPPortForwardObserverDoesNotReturnResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "credential-value", http.StatusUnauthorized)
	}))
	defer server.Close()
	host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	_, err := (&HTTPPortForwardObserver{Client: server.Client(), Port: port}).ObserveLease(context.Background(), host, "wanted")
	if err == nil || err.Error() != "gateway port-forward observation returned HTTP 401" {
		t.Fatalf("error = %v", err)
	}
}
