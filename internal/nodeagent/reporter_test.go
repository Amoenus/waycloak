// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestObservationPublicationTimeoutFitsFreshnessAndServerBudgets(t *testing.T) {
	if ObservationPublicationTimeout <= 5*time.Second || ObservationPublicationTimeout >= 10*time.Second {
		t.Fatalf("observation publication timeout = %s, want >5s and <10s", ObservationPublicationTimeout)
	}
}

func TestReporterClassifiesTimeoutAfterWritingRequest(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	reporter := Reporter{URL: "https://relay.invalid/node-observations/v1/report", TokenFile: tokenFile, Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.WroteRequest != nil {
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := reporter.Report(ctx, Report{APIVersion: ReportAPIVersion})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "phase=response_headers") {
		t.Fatalf("Report error = %v, want classified response-header deadline", err)
	}
}
