# ADR 0012: Port-forward lease identity and observed target binding

Status: Accepted
Date: 2026-07-13

## Context

A provider lease must survive controller restarts without being reassigned by
list order, while gateway DNAT must never target a stale Pod or a new Pod that
reused an address. A label selector is useful workload intent but is not a
stable packet-delivery identity. Service handoff and rolling replicas also make
selector cardinality ambiguous unless the first contract is deliberately
narrow.

## Decision

`PortForwardLease` is namespaced, user-authored intent. Its Kubernetes object
UID is the stable provider-facing identity; names or selector ordering are
never used to renumber leases. The initial target is a non-empty Pod selector
and local port. Exactly one matching Ready Pod is required.

The controller accepts a target only when the Pod is opted into the selected
gateway and a controller-owned `VPNWorkload` binds that exact Pod UID to a
persisted overlay allocation for the same gateway. Status records the observed
Pod UID, `VPNWorkload` reference, overlay address, and local port. A selector
match alone never creates DNAT readiness.

Cross-namespace gateway references use the same
`workloadAccess.namespaceSelector` authorization as Pod admission. Provider
lease acquisition, gateway rules, target binding, delivery, and overall
readiness are separate conditions. Desired registration or a remembered
public port does not make any of them ready.

Service targets and controlled rolling handoff are deferred until their
identity and drain semantics are proven. No lease finalizer is added before a
provider driver has a bounded, tested release/expiry contract.

## Consequences

- Controller restart cannot change lease identity or a persisted allocation.
- A deleted or replaced Pod immediately invalidates target readiness even when
  labels and names are reused.
- Rolling workloads with two Ready matches are explicitly ambiguous rather
  than receiving nondeterministic inbound traffic.
- The first API favors safety over seamless target handoff.
- Provider-specific acquisition remains behind a capability interface and can
  be implemented without changing target identity semantics.

## Alternatives rejected

- Use selector order or Pod name as identity: unstable across scaling and
  replacement.
- Route to any matching Ready Pod: hides cardinality changes and breaks stable
  inbound identity.
- Target a Service initially: endpoint handoff and cross-node policy require a
  separate proven design.
- Add a cleanup finalizer immediately: risks blocking deletion before release
  and expiry behavior exists.
