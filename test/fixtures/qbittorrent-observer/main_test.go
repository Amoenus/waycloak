// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrackerHandlerRecordsExactPort(t *testing.T) {
	output := filepath.Join(t.TempDir(), "tracker-port")
	request := httptest.NewRequest(http.MethodGet, "/announce?port=42000&ip=203.0.113.10", nil)
	response := httptest.NewRecorder()

	trackerHandler(output).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("tracker response status = %d", response.Code)
	}
	observed, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	addressHash := sha256.Sum256([]byte("203.0.113.10"))
	if string(observed) != fmt.Sprintf("42000\n%x\n", addressHash) {
		t.Fatalf("observed port = %q", observed)
	}
}

func TestTrackerHandlerRejectsInvalidPort(t *testing.T) {
	output := filepath.Join(t.TempDir(), "tracker-port")
	request := httptest.NewRequest(http.MethodGet, "/announce?port=0", nil)
	response := httptest.NewRecorder()

	trackerHandler(output).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("tracker response status = %d", response.Code)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("invalid announcement created an observation")
	}
}

func TestTrackerHandlerRejectsMissingAnnouncedAddress(t *testing.T) {
	output := filepath.Join(t.TempDir(), "tracker-port")
	request := httptest.NewRequest(http.MethodGet, "/announce?port=42000", nil)
	response := httptest.NewRecorder()

	trackerHandler(output).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("tracker response status = %d", response.Code)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("missing announced address created an observation")
	}
}

func TestQBitValueUsesBearerTokenAndReadsFields(t *testing.T) {
	const apiKey = "test-only-key"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/api/v2/transfer/info":
			_, _ = io.WriteString(response, `{"dht_nodes":17}`)
		case "/api/v2/app/preferences":
			_, _ = io.WriteString(response, `{"listen_port":42000}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(keyFile, []byte(apiKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--api-key-file=" + keyFile, "--endpoint=" + server.URL}

	dhtNodes, err := qbitValue(args, "dht_nodes")
	if err != nil || dhtNodes != 17 {
		t.Fatalf("dht_nodes = %d, err = %v", dhtNodes, err)
	}
	listenPort, err := qbitValue(args, "listen_port")
	if err != nil || listenPort != 42000 {
		t.Fatalf("listen_port = %d, err = %v", listenPort, err)
	}
}

func TestQBitValueDoesNotReturnResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(response, "provider-private-body")
	}))
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(keyFile, []byte("test-only-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := qbitValue([]string{"--api-key-file=" + keyFile, "--endpoint=" + server.URL}, "dht_nodes")
	if err == nil || strings.Contains(err.Error(), "provider-private-body") {
		t.Fatalf("unexpected error: %v", err)
	}
}
