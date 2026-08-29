// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"flag"
	"time"
)

// Options is the shared, explicit opt-in surface used by Waycloak binaries.
type Options struct {
	ServiceName    string
	OTLPEndpoint   string
	QueueSize      int
	ExportInterval time.Duration
	ExportTimeout  time.Duration
}

func (options *Options) BindFlags(flags *flag.FlagSet, serviceName string) {
	options.ServiceName = serviceName
	flags.StringVar(&options.OTLPEndpoint, "otel-otlp-endpoint", "", "optional OTLP/HTTP endpoint URL; empty disables OTLP export")
	flags.IntVar(&options.QueueSize, "otel-queue-size", defaultQueueSize, "bounded non-blocking telemetry event queue")
	flags.DurationVar(&options.ExportInterval, "otel-export-interval", 15*time.Second, "OTLP metric export interval")
	flags.DurationVar(&options.ExportTimeout, "otel-export-timeout", time.Second, "per-export time budget")
}

func (options Options) Start(ctx context.Context) (*Runtime, error) {
	return Start(ctx, Config(options))
}
