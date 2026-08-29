// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

const instrumentationName = "github.com/Amoenus/waycloak/internal/telemetry"

type Config struct {
	ServiceName    string
	OTLPEndpoint   string
	QueueSize      int
	ExportInterval time.Duration
	ExportTimeout  time.Duration
}

type Runtime struct {
	Recorder Recorder
	cancel   context.CancelFunc
}

func Start(parent context.Context, config Config) (*Runtime, error) {
	if config.OTLPEndpoint == "" {
		return &Runtime{Recorder: noopRecorder{}}, nil
	}
	if strings.TrimSpace(config.ServiceName) == "" {
		return nil, errors.New("telemetry service name is required when export is enabled")
	}
	if !ValidOTLPEndpoint(config.OTLPEndpoint) {
		return nil, errors.New("OTLP endpoint must be an HTTP(S) URL without user information, query, or fragment")
	}
	if config.QueueSize < 1 || config.QueueSize > 4096 {
		return nil, errors.New("telemetry queue size must be between 1 and 4096")
	}
	if config.ExportInterval < time.Second {
		return nil, errors.New("telemetry export interval must be at least one second")
	}
	if config.ExportTimeout <= 0 || config.ExportTimeout > 10*time.Second {
		return nil, errors.New("telemetry export timeout must be between zero and ten seconds")
	}
	ctx, cancel := context.WithCancel(parent)
	exporter := &httpExporter{serviceName: config.ServiceName, endpoint: strings.TrimSuffix(config.OTLPEndpoint, "/"), client: &http.Client{Timeout: config.ExportTimeout}}
	signals := newSignals(exporter, config.QueueSize, config.ExportInterval, config.ExportTimeout)
	runtime := &Runtime{Recorder: signals, cancel: cancel}
	go signals.run(ctx)
	return runtime, nil
}

func ValidOTLPEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (runtime *Runtime) Shutdown(context.Context) error {
	if runtime != nil && runtime.cancel != nil {
		runtime.cancel()
	}
	return nil
}

type noopRecorder struct{}

func (noopRecorder) Record(context.Context, Event) {}

func Noop() Recorder { return noopRecorder{} }

type httpExporter struct {
	serviceName string
	endpoint    string
	client      *http.Client
}

func (exporter *httpExporter) Export(ctx context.Context, events []Event, dropped, failures int64) error {
	now := time.Now().UTC()
	metricErr := exporter.post(ctx, "/v1/metrics", exporter.metrics(events, dropped, failures, now))
	spans := focusedEvents(events)
	if len(spans) == 0 {
		return metricErr
	}
	return errors.Join(metricErr, exporter.post(ctx, "/v1/traces", exporter.traces(spans, now)))
}

func (exporter *httpExporter) post(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, exporter.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := exporter.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("OTLP export returned status %d", response.StatusCode)
	}
	return nil
}

func (exporter *httpExporter) metrics(events []Event, dropped, failures int64, now time.Time) map[string]any {
	operationPoints := make([]any, 0, len(events))
	durationPoints := make([]any, 0, len(events))
	agePoints := make([]any, 0, len(events))
	for _, event := range events {
		attributes := jsonAttributes(boundedAttributes(event))
		operationPoints = append(operationPoints, numberPoint(attributes, now, "asInt", "1"))
		if event.Duration > 0 {
			durationPoints = append(durationPoints, numberPoint(attributes, now, "asDouble", event.Duration.Seconds()))
		}
		if event.Age > 0 {
			agePoints = append(agePoints, numberPoint(attributes, now, "asDouble", event.Age.Seconds()))
		}
	}
	metrics := []any{
		map[string]any{"name": "waycloak.operations", "unit": "{operation}", "sum": map[string]any{"aggregationTemporality": 1, "isMonotonic": true, "dataPoints": operationPoints}},
		map[string]any{"name": "waycloak.operation.duration", "unit": "s", "gauge": map[string]any{"dataPoints": durationPoints}},
		map[string]any{"name": "waycloak.observation.age", "unit": "s", "gauge": map[string]any{"dataPoints": agePoints}},
		map[string]any{"name": "waycloak.telemetry.queue.dropped", "unit": "{event}", "gauge": map[string]any{"dataPoints": []any{numberPoint(nil, now, "asInt", strconv.FormatInt(dropped, 10))}}},
		map[string]any{"name": "waycloak.telemetry.export.failures", "unit": "{attempt}", "gauge": map[string]any{"dataPoints": []any{numberPoint(nil, now, "asInt", strconv.FormatInt(failures, 10))}}},
	}
	return exporter.resource("resourceMetrics", "scopeMetrics", map[string]any{"scope": map[string]any{"name": instrumentationName}, "metrics": metrics})
}

func (exporter *httpExporter) traces(events []Event, now time.Time) map[string]any {
	spans := make([]any, 0, len(events))
	for _, event := range events {
		var traceID [16]byte
		var spanID [8]byte
		_, _ = rand.Read(traceID[:])
		_, _ = rand.Read(spanID[:])
		spans = append(spans, map[string]any{"traceId": hex.EncodeToString(traceID[:]), "spanId": hex.EncodeToString(spanID[:]), "name": "waycloak." + event.Operation, "kind": 1,
			"startTimeUnixNano": unixNano(now.Add(-event.Duration)), "endTimeUnixNano": unixNano(now), "attributes": jsonAttributes(boundedAttributes(event))})
	}
	return exporter.resource("resourceSpans", "scopeSpans", map[string]any{"scope": map[string]any{"name": instrumentationName}, "spans": spans})
}

func (exporter *httpExporter) resource(root, scopes string, scope any) map[string]any {
	return map[string]any{root: []any{map[string]any{"resource": map[string]any{"attributes": []any{map[string]any{"key": "service.name", "value": map[string]any{"stringValue": exporter.serviceName}}}}, scopes: []any{scope}}}}
}

func focusedEvents(events []Event) []Event {
	result := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Result == "failure" || event.Result == "recovered" || event.Operation == "handoff" || event.Operation == "withdrawal" {
			result = append(result, event)
		}
	}
	return result
}

func numberPoint(attributes []any, now time.Time, field string, value any) map[string]any {
	point := map[string]any{"timeUnixNano": unixNano(now), field: value}
	if len(attributes) != 0 {
		point["attributes"] = attributes
	}
	return point
}

func jsonAttributes(attributes []attribute.KeyValue) []any {
	result := make([]any, 0, len(attributes))
	for _, item := range attributes {
		result = append(result, map[string]any{"key": string(item.Key), "value": map[string]any{"stringValue": item.Value.AsString()}})
	}
	return result
}

func unixNano(value time.Time) string { return strconv.FormatInt(value.UnixNano(), 10) }
