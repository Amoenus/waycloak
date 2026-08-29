# OpenTelemetry signal qualification

Issue #246 introduces one privacy-bounded OpenTelemetry instrument set for
diagnostic gateway and adapter signals. It does not change readiness, packet
programming, reconciliation results, Kubernetes Conditions, or Events.

## Qualified dependency set

The source refresh on 2026-08-29 confirmed OpenTelemetry Go v1.46.0 as the
latest stable upstream release. Waycloak pins its stable attribute API and uses
the standard OTLP/HTTP JSON protocol for export. The dependency inventory
records its Apache-2.0 license, upstream
security policy, SBOM/provenance/reproducibility gates, compatibility tests, and
runtime evidence.

## Runtime contract

- With both exporter addresses empty, `Start` returns a true no-op recorder. It
  creates no SDK provider, exporter, listener, worker, or collector dependency.
- Enabled instrumentation writes to a fixed-size queue without waiting. A full
  queue drops the diagnostic event and increments
  `waycloak_telemetry_queue_dropped_total`.
- OTLP metrics and focused traces share a 256-event queue, 32-event batch,
  15-second interval, and one-second default export budget. Only failures,
  recovery, handoff, and withdrawal create focused spans.
- Metric or trace export errors increment the bounded
  `waycloak_telemetry_export_failures_total` instrument. They never propagate
  into a reconciliation or readiness result.
- A standard optional collector can translate this same OTLP instrument set to
  Prometheus exposition; Waycloak does not implement a second operational
  metric model. The controller's existing aggregate Kubernetes-status
  projection remains a separate view of the authoritative API contract.
- Helm leaves OTLP disabled by default. Setting
  `observability.openTelemetry.otlpEndpoint` propagates the reviewed queue and
  timeout settings to gateway components. No collector is installed.

## Privacy and cardinality

The event type can represent only fixed `component`, `operation`, `result`,
`phase`, transport, DNS question type, and failure-class enums plus durations.
Unknown values are replaced with stable fallbacks before SDK recording. There
is no field for a namespace, workload or Pod name, UID, lease identity, port,
address, endpoint, provider, domain, torrent, credential, image digest, error
message, or arbitrary generation/event identifier. Focused traces use the same
attributes and never attach packet contents.

## Source evidence

The tests prove disabled no-op construction, non-blocking queue saturation,
drop accounting, exporter-failure accounting, normalization of hostile dynamic
values, standard OTLP metric/trace paths and JSON payloads. Focused
gateway, port-forward, qBittorrent-adapter, Helm, and Linux-target build suites
exercise the integration.

The initially tested gRPC plus in-process Prometheus exporters added 9.8-15.2
MiB (57-126%) to stripped Linux/amd64 binaries and were rejected as too heavy.
The official OTLP/HTTP protobuf exporters still added 8.8-14.8 MiB (56-113%)
and were also rejected. The qualified path serializes the bounded schema using
standard-library OTLP/HTTP JSON and leaves Prometheus translation to an
optional collector.

Against exact baseline `fdbe61f`, stripped Linux/amd64 binaries grew by 155,648
bytes (2.00%) for the gateway agent, 204,800 bytes (0.77%) for the gateway
runtime, and 200,704 bytes (0.76%) for the qBittorrent adapter.

On the qualification host (Windows/amd64, AMD Ryzen 9 9950X3D), three benchmark
runs measured the disabled recorder at 0.28-0.29 ns/op and zero allocations. A
full enabled queue measured 36-40 ns/op and zero allocations; this is the worst
non-blocking admission path, not exporter work. Exact Linux image size/RSS,
collector-loss behavior, and the live failure/renewal timeline remain release
and soak gates before #246 can close.
