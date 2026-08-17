// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestResolvingObservationDialContextRecoversBeforePublicationDeadline(t *testing.T) {
	lookups := 0
	dials := 0
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	dial := resolvingObservationDialContext(func(context.Context, string) ([]net.IPAddr, error) {
		lookups++
		if lookups < 3 {
			return nil, context.DeadlineExceeded
		}
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}, nil
	}, func(_ context.Context, _, address string) (net.Conn, error) {
		dials++
		if address != "192.0.2.1:9443" {
			t.Fatalf("resolved dial address = %q", address)
		}
		return client, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), ObservationPublicationTimeout)
	defer cancel()
	connection, err := dial(ctx, "tcp", "relay.invalid:9443")
	if err != nil {
		t.Fatal(err)
	}
	if connection != client || lookups != 3 || dials != 1 {
		t.Fatalf("connection, lookups, dials = %v, %d, %d; want one dial after 3 lookups", connection, lookups, dials)
	}
}

func TestResolvingObservationDialContextHonorsParentDeadline(t *testing.T) {
	lookups := 0
	dial := resolvingObservationDialContext(func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		lookups++
		<-ctx.Done()
		return nil, ctx.Err()
	}, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dial must not run after lookup deadline")
		return nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := dial(ctx, "tcp", "relay.invalid:9443")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial error = %v, want deadline exceeded", err)
	}
	if lookups != 1 || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("lookups, elapsed = %d, %s; retry escaped parent deadline", lookups, time.Since(started))
	}
}

func TestResolvingObservationDialContextDoesNotRetryPermanentNameError(t *testing.T) {
	lookups := 0
	dial := resolvingObservationDialContext(func(context.Context, string) ([]net.IPAddr, error) {
		lookups++
		return nil, &net.DNSError{Err: "no such host", Name: "relay.invalid", IsNotFound: true}
	}, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dial must not run after permanent name error")
		return nil, nil
	})
	_, err := dial(context.Background(), "tcp", "relay.invalid:9443")
	if err == nil || lookups != 1 {
		t.Fatalf("error, lookups = %v, %d; want one permanent lookup failure", err, lookups)
	}
}

func TestResolvingObservationDialContextDoesNotRetryConnectionFailure(t *testing.T) {
	lookups := 0
	dials := 0
	dial := resolvingObservationDialContext(func(context.Context, string) ([]net.IPAddr, error) {
		lookups++
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}, nil
	}, func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, syscall.ECONNREFUSED
	})
	_, err := dial(context.Background(), "tcp", "relay.invalid:9443")
	if !errors.Is(err, syscall.ECONNREFUSED) || lookups != 1 || dials != 1 {
		t.Fatalf("error, lookups, dials = %v, %d, %d; connection failure was retried", err, lookups, dials)
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
