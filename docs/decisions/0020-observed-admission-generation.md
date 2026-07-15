# ADR 0020: Observed admission generation gates webhook upgrades

Status: Accepted
Date: 2026-07-15

## Context

Waycloak runs multiple controller/webhook replicas with zero-unavailable
rolling updates. Kubernetes can route an opted-in Pod admission request to an
old replica while a new controller and injected agent identity are rolling
out. Per-replica deterministic mutation does not prevent this mixed-release
result.

The gate must react before controller-runtime cache propagation, must leave
unannotated Pods outside the failure domain, and must not require protected
workloads to copy an installer-generated value into their templates.

## Decision

The Helm chart hashes the immutable controller and agent image identities into
one admission generation and writes it to a stable ConfigMap. Each webhook
process receives the same locally compiled generation as an argument and reads
the desired ConfigMap through controller-runtime's uncached API reader.

An opted-in mutating or validating request is accepted only when local and
desired generations match. The same comparison controls replica readiness.
Successful mutation records the applied generation in an internal Pod
annotation, and validation requires that exact value. The API-server webhook
match condition continues to exclude unannotated Pods.

The Deployment uses `maxUnavailable: 0` and `maxSurge: 100%`. A desired
generation change can make every old replica unready simultaneously, so the
full new replica set must be allowed to start before stale Pods are removed.

## Consequences

- A stale webhook can reject an opted-in request but cannot silently inject its
  old agent identity.
- Operators can compare the desired ConfigMap, controller Pod annotation, and
  injected Pod annotation without reading webhook logs.
- Admission becomes fail-closed when the desired ConfigMap or an uncached API
  read is unavailable.
- Agent-changing GitOps updates may produce temporary Pod-creation failures;
  the documented control-plane-first sequence remains the low-disruption path.
- Generation-changing upgrades temporarily require capacity for twice the
  configured controller replica count.

## Alternatives rejected

- Rely only on two-phase operator sequencing: safe when followed, but does not
  prevent a concurrent GitOps workload rollout.
- Use the controller-runtime cache or a projected ConfigMap: propagation delay
  leaves a window in which a stale replica can still admit a Pod.
- Select webhook Service endpoints only by a generation label: endpoint
  updates are useful but do not close the per-request race by themselves.
- Put the desired generation on every workload template: exposes an
  installer-owned internal value as a user contract and complicates ordinary
  workload authoring.

## Supersession

This extends ADR 0002's requirement that admission injection be versioned,
idempotent, observable, and protected against bypass.
