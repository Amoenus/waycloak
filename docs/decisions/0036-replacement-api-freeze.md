# ADR 0036: Replacement API freeze

Status: Accepted
Date: 2026-07-26

## Context

ADRs 0025–0035 establish the clean break, roles, route enrollment, consent,
status, conformance, CRD lifecycle, bootstrap, upstream boundaries, CNI proof,
and node-agent trust. Generated replacement APIs still require one exact answer
for version, Kubernetes minimum, field ownership, reference shapes, list keys,
immutability, deletion, and admission. Leaving any of those to reconciler
implementation would make security and compatibility accidental.

Kubernetes 1.36 makes `MutatingAdmissionPolicy` stable. Gateway API
`ReferenceGrant` is GA, but adding its CRDs solely for a relationship already
owned by `VPNGateway.allowedRoutes` would create an unnecessary Core dependency.

## Decision

The replacement group/version is `networking.waycloak.io/v1beta1` and requires
Kubernetes 1.36 or newer. The six kinds and scopes are:

| Kind | Scope | Spec owner |
| --- | --- | --- |
| `VPNGatewayClass` | cluster | distribution |
| `VPNGateway` | namespace | cluster/network operator |
| `VPNEgressRoute` | namespace | workload owner |
| `VPNWorkloadBinding` | namespace | controller only |
| `PortForwardLease` | namespace | workload owner |
| `WorkloadAdapter` | namespace | operator |

All user-visible resources use `Accepted`, `ResolvedRefs`, `Programmed`, and
`Ready`; route status additionally has bounded atomic parent entries logically
identified by the explicit parent reference and immutable controller name. The
list cannot be map-keyed because the parent reference is an object. Core route
spec has exactly one map-keyed parent and status has at most one parent entry.

Every reference has explicit existence, compatibility, consent, revocation and
privacy semantics. Gateway-side `allowedRoutes` is the sole Core
cross-namespace consent relationship. Other Core references are local or
cluster-scoped. Core neither installs `ReferenceGrant` nor defines a local grant
kind and does not claim Gateway API conformance.

Controllers use distinct server-side field managers for status, generated
dependents, and controller-owned bindings. They never force ownership of user
spec. The credential-free node agent never writes the Kubernetes API. Owner
references are same-scope real dependencies only. Finalizers exist only for
bounded external data-plane or provider cleanup and have explicit quarantine
outcomes.

Core has no admission webhook. Stable mutating policy may add only CNI-capable
node placement metadata to enrolled Pods. Stable validating policy rejects
alpha markers and non-controller binding writes. Dynamic references reconcile
asynchronously, and chained CNI remains the independent packet-security gate.

The exact reviewed fields, list types/bounds, references, immutable fields,
features, reasons, finalizers, and field managers are machine-readable in
[`replacement-api-freeze.json`](../api/replacement-api-freeze.json). CI audits
that contract before #128 generates CRDs or Go types.

ADR 0038 amends the pre-release binding contract with a required,
controller-authored `spec.network` projection. This closes the privileged
node-agent authority gap without adding Gateway/ConfigMap/Secret reads or
trusting caller configuration.

## Consequences

- The first replacement implementation carries beta compatibility discipline
  from its initial release.
- Kubernetes 1.35 proof remains feasibility evidence but is not a published
  stable support row.
- Core avoids webhook TLS and Gateway API CRD dependencies without weakening
  creation-time or reference enforcement.
- Port forwarding and adapters have stable object boundaries but cannot be
  advertised until their Extended conformance work passes.
- Any generated-schema divergence requires an ADR amendment and renewed API
  review; reconciler convenience cannot silently change the contract.

## Alternatives rejected

- Start another alpha version: defers compatibility decisions already resolved
  by the clean-break review and delays the required beta stability cycle.
- Support Kubernetes 1.35 and retain a mutation webhook: expands the Core
  failure and certificate surface for one older row.
- Require Gateway API for `ReferenceGrant`: adds a cluster API dependency with
  no Core reference that needs it.
- Let controllers infer fields, defaults or ownership: makes round trips,
  conflicts and security behavior implementation-specific.

## Related decisions

- [ADRs 0025–0033](README.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0035](0035-node-agent-trust-and-local-protocol.md)
- [ADR 0038](0038-binding-network-projection-and-node-observations.md)
