# ADR 0018: Out-of-process workload adapter protocol

Status: Accepted
Date: 2026-07-15

## Context

ADRs 0015 and 0016 keep fixed-port translation generic and allow a narrow
adapter when an application must apply a provider-assigned port through a
proprietary API. The qBitTorrent adapter proves the boundary, but its Go
packages and container conventions are not yet a public extension contract.

Third-party adapters need stable discovery, generation, expiry,
acknowledgement, readiness, security, and packaging rules. In-process Go
plugins would bind adapters to a compiler and Waycloak implementation version,
run untrusted application logic inside a privileged component, and prevent
language-neutral implementations. Allowing a workload annotation to inject
an arbitrary image would create a supply-chain and admission boundary with no
operator trust decision.

## Decision

Workload adapters are separate OCI containers implementing a versioned,
language-neutral Pod-local protocol. They never run inside the controller,
webhook, gateway manager, or routing-agent process.

The public adapter protocol consists of:

- the neutral lease JSON schema exposed by file and loopback HTTP;
- exact selection by lease identity or an unambiguous declared selector;
- expiry and generation semantics;
- an acknowledgement request bound to Pod UID, lease identity, generation,
  and applied application port;
- readiness and error behavior for missing, stale, duplicate, or rejected
  leases;
- retry and bounded-backoff expectations.

The protocol is defined independently of the Waycloak Go packages. Waycloak
will publish schemas, fixtures, a black-box conformance suite, and a minimal
sample adapter. Language-specific helper libraries are optional conveniences.

Adapters are selected through explicit workload intent and a cluster-scoped,
operator-authored `WorkloadAdapter`. The Pod template names that trust record
and an existing adapter container; admission requires the container image to
exactly match the trusted immutable digest and validates its least-privilege
security posture. This deliberately keeps application configuration in the
workload while preventing an annotation from making Waycloak trust or inject
an arbitrary registry reference.

Application credentials and configuration remain workload-owned. Only
explicitly selected mounts or environment references may reach an adapter.
The adapter receives no Kubernetes API token, VPN credentials, Linux
capabilities, host namespace, or Waycloak control-plane credential.

Released reference adapters are independent, signed multi-architecture OCI
artifacts with SBOM, provenance, protocol compatibility metadata, and
immutable references in the release manifest. qBitTorrent is the first
reference adapter and remains application-specific; its semantics do not enter
the core protocol.

## Consequences

- Adapter authors can use any language and depend only on a small local
  protocol.
- Adapter failure can regress delivery readiness without compromising the
  fail-closed packet path.
- Protocol versions can evolve independently of internal Go refactors.
- Plug-and-play means explicit selection of a trusted conformant OCI artifact,
  not arbitrary code injection.
- Waycloak must publish conformance tooling and define version negotiation,
  installation, and configuration projection.
- Most workloads continue to need no adapter at all.

## Alternatives rejected

- Go `plugin` modules: platform/compiler coupling and an unsafe in-process
  trust boundary.
- Application-specific controller reconcilers: spreads proprietary APIs and
  credentials into the control plane.
- An unrestricted adapter-image annotation: lets workload authors introduce
  arbitrary sidecars through a privileged webhook.
- Treat OCI labels alone as an API: metadata cannot define runtime generation,
  expiry, acknowledgement, or failure behavior.
- Require every adapter to import Waycloak Go internals: excludes other
  languages and makes internal refactoring a breaking public change.
