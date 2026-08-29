// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRejectsUnboundedAndSensitiveDimensions(t *testing.T) {
	event := normalize(Event{Component: "tenant-a/pod-uid", Operation: "49152", Result: "https://private.example", Phase: "lease-uid", Transport: "quic", QueryType: "secret.example", FailureClass: "token", Duration: -time.Second, Age: -time.Second})
	if event.Component != "telemetry" || event.Operation != "export" || event.Result != "failure" || event.Phase != "none" || event.Transport != "none" || event.QueryType != "none" || event.FailureClass != "other" {
		t.Fatalf("unbounded dimensions were not normalized: %#v", event)
	}
	if event.Duration != 0 || event.Age != 0 {
		t.Fatalf("negative values were not clamped: %#v", event)
	}
}

func TestRecordDropsInsteadOfBlockingWhenQueueIsSaturated(t *testing.T) {
	signals := newSignals(nil, 1, time.Second, time.Second)
	event := Event{Component: "gateway_agent", Operation: "reconcile", Result: "success"}
	signals.Record(context.Background(), event)
	completed := make(chan struct{})
	go func() {
		signals.Record(context.Background(), event)
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("record blocked on a saturated telemetry queue")
	}
	if signals.dropped.Load() != 1 {
		t.Fatalf("dropped=%d, want 1", signals.dropped.Load())
	}
}

func TestStartIsNoopByDefault(t *testing.T) {
	runtime, err := Start(context.Background(), Config{ServiceName: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.cancel != nil {
		t.Fatal("disabled telemetry allocated runtime infrastructure")
	}
	runtime.Recorder.Record(context.Background(), Event{Component: "gateway_agent", Operation: "reconcile", Result: "success"})
}

func TestOTLPJSONUsesOnlyBoundedAttributesAndStandardPaths(t *testing.T) {
	paths := make(chan string, 2)
	payloads := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		paths <- request.URL.Path
		var value any
		if err := json.NewDecoder(request.Body).Decode(&value); err != nil {
			t.Error(err)
		}
		body, _ := json.Marshal(value)
		payloads <- body
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	exporter := &httpExporter{serviceName: "waycloak-test", endpoint: server.URL, client: server.Client()}
	event := normalize(Event{Component: "pod/a", Operation: "49152", Result: "secret.example", Phase: "uid", Transport: "quic", QueryType: "private.example", FailureClass: "credential", Duration: time.Second})
	if err := exporter.Export(context.Background(), []Event{event}, 2, 3); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		path := <-paths
		seen[path] = true
		body := string(<-payloads)
		if path == "/v1/metrics" && (!strings.Contains(body, `"resourceMetrics"`) || !strings.Contains(body, `"waycloak.operations"`) || !strings.Contains(body, `"aggregationTemporality":1`)) {
			t.Fatalf("metric payload lacks the bounded OTLP schema: %s", body)
		}
		if path == "/v1/traces" && (!strings.Contains(body, `"resourceSpans"`) || !strings.Contains(body, `"traceId"`) || !strings.Contains(body, `"spanId"`)) {
			t.Fatalf("trace payload lacks the bounded OTLP schema: %s", body)
		}
		for _, forbidden := range []string{"pod/a", "49152", "secret.example", "uid", "quic", "private.example", "credential"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("OTLP payload contains unbounded value %q: %s", forbidden, body)
			}
		}
	}
	if !seen["/v1/metrics"] || !seen["/v1/traces"] {
		t.Fatalf("OTLP paths = %#v", seen)
	}
}

func TestSlowOrFailedExporterCannotBlockRecord(t *testing.T) {
	exporter := blockingExporter{}
	signals := newSignals(exporter, 1, time.Second, 10*time.Millisecond)
	signals.Record(context.Background(), Event{Component: "gateway_agent", Operation: "reconcile", Result: "success"})
	started := time.Now()
	for range 1000 {
		signals.Record(context.Background(), Event{Component: "gateway_agent", Operation: "reconcile", Result: "success"})
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("recording waited for the exporter")
	}
	signals.flush(context.Background(), nil)
	if signals.exportFailures.Load() != 1 {
		t.Fatalf("export failure count=%d, want 1", signals.exportFailures.Load())
	}
}

func TestValidOTLPEndpointRejectsCredentialsAndQuery(t *testing.T) {
	for _, value := range []string{"", "grpc://collector:4317", "https://user:secret@collector:4318", "https://collector:4318?token=secret", "https://collector:4318/#fragment"} {
		if ValidOTLPEndpoint(value) {
			t.Fatalf("accepted unsafe endpoint %q", value)
		}
	}
	if !ValidOTLPEndpoint("https://collector.example:4318") {
		t.Fatal("rejected valid HTTPS OTLP endpoint")
	}
}

func BenchmarkNoopRecord(b *testing.B) {
	recorder := Noop()
	event := Event{Component: "gateway_agent", Operation: "reconcile", Result: "success"}
	for b.Loop() {
		recorder.Record(context.Background(), event)
	}
}

func BenchmarkBoundedQueueRecordWhenSaturated(b *testing.B) {
	signals := newSignals(nil, 1, time.Second, time.Second)
	event := Event{Component: "gateway_agent", Operation: "reconcile", Result: "success"}
	signals.Record(context.Background(), event)
	b.ResetTimer()
	for b.Loop() {
		signals.Record(context.Background(), event)
	}
}

type blockingExporter struct{}

func (blockingExporter) Export(ctx context.Context, _ []Event, _, _ int64) error {
	<-ctx.Done()
	return errors.New("export failed")
}
