// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package telemetry defines Waycloak's single, privacy-bounded OpenTelemetry
// signal model. Signals are diagnostic only; Kubernetes Conditions, Events,
// and data-plane observations remain authoritative.
package telemetry

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultQueueSize = 256
	defaultBatchSize = 32
)

var (
	allowedComponents = set("gateway_agent", "gateway_runtime", "qbittorrent_adapter", "controller", "telemetry")
	allowedOperations = set("reconcile", "dns_probe", "engine_observe", "provider_observe", "mapping_refresh", "rules_apply", "delivery", "acknowledgement", "adapter_apply", "adapter_observe", "withdrawal", "handoff", "export")
	allowedResults    = set("success", "failure", "withdrawn", "recovered", "stale", "dropped")
	allowedPhases     = set("none", "cluster", "external", "capability", "acquire", "renew", "translate", "apply", "observe", "acknowledge", "withdraw", "recovery")
	allowedTransports = set("none", "udp", "tcp")
	allowedQueryTypes = set("none", "a", "aaaa")
	allowedFailures   = set("none", "timeout", "unavailable", "invalid", "stale", "denied", "saturated", "internal", "other")
)

type Recorder interface {
	Record(context.Context, Event)
}

// Event has no fields for resource identity or user data. Its dimensions are
// normalized to fixed enums before queue admission and export.
type Event struct {
	Component    string
	Operation    string
	Result       string
	Phase        string
	Transport    string
	QueryType    string
	FailureClass string
	Duration     time.Duration
	Age          time.Duration
}

func Emit(recorder Recorder, ctx context.Context, event Event) {
	if recorder != nil {
		recorder.Record(ctx, event)
	}
}

func Result(err error) string {
	if err != nil {
		return "failure"
	}
	return "success"
}

func FailureClass(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "timeout"
		}
		return "unavailable"
	}
	return "other"
}

type batchExporter interface {
	Export(context.Context, []Event, int64, int64) error
}

type Signals struct {
	queue          chan Event
	dropped        atomic.Int64
	exportFailures atomic.Int64
	exporter       batchExporter
	flushInterval  time.Duration
	exportTimeout  time.Duration
}

func newSignals(exporter batchExporter, queueSize int, flushInterval, exportTimeout time.Duration) *Signals {
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	return &Signals{queue: make(chan Event, queueSize), exporter: exporter, flushInterval: flushInterval, exportTimeout: exportTimeout}
}

// Record is deliberately non-blocking.
func (signals *Signals) Record(_ context.Context, event Event) {
	if signals == nil {
		return
	}
	event = normalize(event)
	select {
	case signals.queue <- event:
	default:
		signals.dropped.Add(1)
	}
}

func (signals *Signals) run(ctx context.Context) {
	interval := signals.flushInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	batch := make([]Event, 0, defaultBatchSize)
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-signals.queue:
			batch = append(batch, event)
			if len(batch) == cap(batch) {
				signals.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			signals.flush(ctx, batch)
			batch = batch[:0]
		}
	}
}

func (signals *Signals) flush(ctx context.Context, batch []Event) {
	if signals.exporter == nil {
		return
	}
	timeout := signals.exportTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	exportCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := signals.exporter.Export(exportCtx, batch, signals.dropped.Load(), signals.exportFailures.Load()); err != nil {
		signals.exportFailures.Add(1)
	}
}

func boundedAttributes(event Event) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("waycloak.component", event.Component),
		attribute.String("waycloak.operation", event.Operation),
		attribute.String("waycloak.result", event.Result),
		attribute.String("waycloak.phase", event.Phase),
		attribute.String("network.transport", event.Transport),
		attribute.String("dns.question.type", event.QueryType),
		attribute.String("error.type", event.FailureClass),
	}
}

func normalize(event Event) Event {
	event.Component = bounded(event.Component, allowedComponents, "telemetry")
	event.Operation = bounded(event.Operation, allowedOperations, "export")
	event.Result = bounded(event.Result, allowedResults, "failure")
	event.Phase = bounded(event.Phase, allowedPhases, "none")
	event.Transport = bounded(event.Transport, allowedTransports, "none")
	event.QueryType = bounded(event.QueryType, allowedQueryTypes, "none")
	event.FailureClass = bounded(event.FailureClass, allowedFailures, "other")
	if event.Duration < 0 {
		event.Duration = 0
	}
	if event.Age < 0 {
		event.Age = 0
	}
	return event
}

func bounded(value string, allowed map[string]struct{}, fallback string) string {
	if _, ok := allowed[value]; ok {
		return value
	}
	return fallback
}

func set(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
