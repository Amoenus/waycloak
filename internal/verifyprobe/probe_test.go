// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package verifyprobe

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunUsesExplicitCAAndWritesOnlyValidatedAddress(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("203.0.113.9\n"))
	}))
	defer server.Close()
	directory := t.TempDir()
	caPath := filepath.Join(directory, "ca.crt")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(directory, "termination.log")
	values := map[string]string{"PROBE_URL": server.URL, "PROBE_CA_FILE": caPath, "TERMINATION_LOG_PATH": resultPath, "PROBE_HOLD_AFTER_SUCCESS": "50ms"}
	started := time.Now()
	if err := Run(context.Background(), func(name string) string { return values[name] }); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("success hold returned too early: %s", elapsed)
	}
	contents, err := os.ReadFile(resultPath)
	if err != nil || string(contents) != "203.0.113.9" {
		t.Fatalf("unexpected result %q: %v", contents, err)
	}
}

func TestRunRejectsUnsafeEndpointAndNonAddressResponse(t *testing.T) {
	for _, hold := range []string{"0s", "invalid", "31s"} {
		values := map[string]string{"PROBE_HOLD_AFTER_SUCCESS": hold}
		if err := Run(context.Background(), func(name string) string { return values[name] }); err == nil {
			t.Fatalf("unsafe success hold %q was accepted", hold)
		}
	}
	for _, probeURL := range []string{"http://example.invalid", "https://user@example.invalid", "https://example.invalid/#fragment"} {
		values := map[string]string{"PROBE_URL": probeURL}
		if err := Run(context.Background(), func(name string) string { return values[name] }); err == nil {
			t.Fatalf("unsafe endpoint %q was accepted", probeURL)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Repeat("not-an-address", 20)))
	}))
	defer server.Close()
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"PROBE_URL": server.URL, "PROBE_CA_FILE": caPath}
	if err := Run(context.Background(), func(name string) string { return values[name] }); err == nil {
		t.Fatal("invalid observer response was accepted")
	}
}
