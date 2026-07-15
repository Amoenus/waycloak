# ADR 0021: Gateway membership uses desired and applied generations

Status: Accepted
Date: 2026-07-15

## Context

The controller updates gateway membership in a ConfigMap, but kubelet projects
that update into the running singleton gateway asynchronously. Pod readiness
alone cannot distinguish the old valid membership from the desired new one.
Replacement workloads correctly remain fail closed during the gap, while
operators previously needed kernel inspection to tell normal projection delay
from a stuck manager.

## Decision

The controller hashes the canonical stable member records, including identity,
overlay address, and observed underlay address, into a desired membership
generation published both in `gateway.json` and a separate ConfigMap key.

The gateway manager validates the generation against the complete document and
advances its last-known-good applied generation only after the network,
forwarding, port-rule, and DNS reconciliation for that document succeeds. It
exposes that value through a read-only tokenless HTTP observation. Invalid or
partial projections do not invoke reconcilers and cannot replace the last
known-good applied generation or kernel state.

The controller reads the exact serving Pod's observation. `MembershipApplied`,
`OverlayReady`, and overall `Ready` remain false while desired and applied
generations differ or observation fails. The controller emits a transition
event and polls at a bounded interval until they match.

## Consequences

- Status identifies projection delay without exposing credentials or requiring
  manager Kubernetes API access.
- Adding, removing, or replacing one member advances the hash without deriving
  or renumbering any stable allocation.
- A lease-only change does not change the membership generation.
- The controller performs short direct Pod observations only while evaluating
  gateway health and requeues while membership is pending.
- The last-known-good applied generation remains visible when a later document
  is malformed, while readiness still fails closed on the configuration error.

## Alternatives rejected

- Treat the ConfigMap resource version as applied state: it observes API
  publication, not kubelet projection or kernel reconciliation.
- Treat manager Pod readiness as current membership: a valid old projection can
  remain ready during the delay.
- Put Kubernetes credentials in the manager: unnecessarily expands the gateway
  trust boundary.
- Rebuild or restart the gateway for every membership change: disrupts the
  singleton tunnel and existing allocations.

## Supersession

This refines ADR 0005's allocation startup handshake and ADR 0009's gateway
manager ownership boundary.
