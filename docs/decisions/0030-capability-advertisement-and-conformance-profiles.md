# ADR 0030: Capability advertisement and conformance profiles

Status: Accepted by issue #127
Date: 2026-07-26

## Context

VPN engines, providers, Kubernetes versions, CNIs, architectures, and Waycloak
data-plane backends support different feature sets. Desired configuration is
not proof of runtime capability, and a turnkey product cannot ask users to
discover compatibility through failed workloads. Conversely, claiming support
from kernel version, provider name, or image presence is unsafe.

Waycloak already requires backend-neutral packet tests and explicit rejection
without fallback. Stable API and installation need a machine-readable form of
that contract.

## Decision

Waycloak publishes versioned conformance profiles and feature identifiers.

The first frozen identifiers are
`networking.waycloak.io/FailClosedEgress`,
`networking.waycloak.io/TCP`, `networking.waycloak.io/UDP`,
`networking.waycloak.io/DNSContainment`,
`networking.waycloak.io/GatewayReplacementRecovery`, and
`networking.waycloak.io/NodeRestartRecovery`. Optional capabilities use
`networking.waycloak.io/PortForwardServiceSingleActive` and
`networking.waycloak.io/WorkloadAdapter`.

The internal `Core-v1` profile names the baseline certification suite; it is not
a product edition or release channel. It includes explicit workload opt-in, TCP/UDP VPN egress,
fail-closed startup and runtime loss, contained UDP/TCP DNS, stable allocation,
gateway replacement recovery, conditions/events, credential isolation, and
safe removal of protection.

Optional features such as provider port forwarding, application-adapter
delivery, additional engines/providers, and Service targets are advertised and
tested independently on the same release and class. They do not create another
profile or data plane. Experimental features have not yet passed the baseline
lifecycle contract. CNI creation-time enforcement is baseline; eBPF may be one
implementation and is not a workload API.

`VPNGatewayClass.status.supportedFeatures` reports what its controller and
release can implement. `VPNGateway.status.supportedFeatures` and conditions
report the subset observed for that concrete engine, provider, protocol, and
cluster placement. A resource requiring an unsupported feature is rejected
with `Accepted=False`; it is never accepted with degraded or fallback behavior.

Every release publishes reproducible conformance reports keyed by release
digest, Kubernetes version, CNI/runtime, architecture, engine/provider mode,
and advertised feature set. Fake-provider results and credentialed real-provider
evidence are distinguished. Claims in documentation and release metadata must
be derivable from those reports.

## Consequences

- Installers can select safe defaults before creating protected workloads.
- Multiple implementations can share a public API without pretending to have
  identical capabilities.
- The test matrix and release artifacts grow with each supported capability.
- Runtime capability loss can be represented separately from unsupported
  desired intent.
- Experimental success cannot be advertised as baseline conformance.

## Alternatives rejected

- Infer capability from configured provider or engine name: desired text does
  not prove a usable tunnel or provider feature.
- Document support only in prose: not consumable by admission, automation, or
  cluster diagnostics.
- Accept unsupported configuration and ignore fields: makes protection
  behavior ambiguous.
- Require every implementation to support every feature: prevents portable
  baseline behavior and honest optional capabilities.

## Related decisions

- [ADR 0003](0003-gluetun-provider-interface.md)
- [ADR 0018](0018-workload-adapter-protocol.md)
- [ADR 0025](0025-api-stability-and-feature-channels.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
