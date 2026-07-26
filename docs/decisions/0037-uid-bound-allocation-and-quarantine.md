# ADR 0037: UID-bound allocation and durable quarantine

Status: Accepted
Date: 2026-07-26

## Context

The replacement API freezes `VPNWorkloadBinding` as the controller-owned,
exact-Pod-UID desired-state record. Issue #132 must make allocation persistent
before CNI success, collision-safe across replicas and restarts, and safely
quarantined after unconfirmed withdrawal. The public API intentionally has no
allocation-ledger kind, and the alpha allocation ConfigMap is teardown evidence
only. A hidden in-memory counter or list position would repeat the alpha race;
putting an implementation-specific pool in route intent would violate role
ownership.

## Decision

The gateway controller publishes its explicitly configured workload pool as a
typed `VPNGateway.status.addresses` entry named
`networking.waycloak.io/OverlayCIDR`. It derives that observation from reviewed
native configuration; the binding controller never guesses a pool or reads VPN
credentials. Initial Core allocation supports IPv4 `/16` through `/29`. The
network address, first host used by the gateway, and broadcast address are not
allocatable.

For every exact `(gateway UID, Pod UID)` pair, the binding controller derives a
stable opaque allocation identity and Pod-UID-derived binding name using a
domain-separated SHA-256 digest. Neither identity depends on object order,
names, informer state, or a sequential counter.

Before creating the binding, the controller atomically creates a
`coordination.k8s.io/v1 Lease` in the gateway namespace for the candidate
address. The Lease:

- is owned by the exact same-namespace `VPNGateway` UID;
- is named from the gateway UID and canonical address;
- records the exact gateway UID, Pod UID, address, allocation identity, and
  `Active` or `Quarantined` state;
- has no expiry and contains no credential, provider configuration, route
  configuration, or data-plane configuration.

Lease `CREATE` is the concurrent collision boundary. A matching active Lease
is crash recovery; a conflicting Lease advances deterministic probing. Pool
exhaustion publishes no binding, so bounded CNI lookup fails with denial still
installed. The Lease is internal allocation coordination, not an alpha-style
ConfigMap projection or CNI handshake. The node agent has no Lease RBAC.

Desired, applied, and live state remain separate. Binding generation is desired
state; `status.appliedGeneration` is authenticated node-applied state; exact
observed Pod UID, gateway UID, node, node boot, agent instance, and observation
time are live state. `Ready=True` requires all three to be current. The local
CNI response carries binding UID, binding generation, gateway UID, and matching
allocation/gateway generations; missing, stale, or mismatched identity fails
`ADD` while retaining deny-first state.

Deletion immediately publishes `Ready=False/Deleting`. The bounded finalizer
releases an exact reservation only when a fresh, exact authenticated agent
observation confirms that no generation remains applied. Absence of an applied
status is not proof of cleanup. If withdrawal is still unconfirmed at
ten minutes, the controller creates or updates the exact reservation to
`Quarantined` before releasing the finalizer. Missing reservation state is
recreated as quarantine; a conflicting reservation blocks finalizer release.
A quarantined identity cannot acquire another address. Issue #133 supplies the
authenticated node cleanup observation used by the normal applied-state path.

No alpha `VPNWorkload`, allocation ConfigMap, annotation, lease, or runtime
state is read or converted.

## Consequences

- Controller replicas can allocate concurrently without sharing an address.
- Restart recovery uses authoritative persisted reservations and exact UIDs,
  not informer freshness or list order.
- Quarantine can consume pool capacity until operator-visible cleanup tooling
  proves safe release; exhaustion is fail closed rather than self-healing by
  unsafe reuse.
- Gateway deletion removes only reservations in that gateway failure domain;
  the deleted UID can never authorize a replacement gateway's bindings.
- Kubernetes Lease is used for its native coordination semantics without
  becoming a public Waycloak configuration surface.

## Alternatives rejected

- Reuse the alpha allocation ConfigMap: imports alpha runtime state and repeats
  an eventually consistent startup handshake.
- Allocate from cached binding list order: permits collisions and renumbering
  across concurrency, restart, and stale informers.
- Store a global in-memory or ConfigMap counter: adds a hidden singleton and
  cannot represent per-address quarantine safely.
- Add a public allocation CRD: expands the frozen persona API for internal
  coordination that Kubernetes Lease already models.
- Reuse an address after finalizer timeout: can cross-deliver traffic while
  stale node or gateway state remains live.

## Related decisions

- [ADR 0029](0029-common-status-and-condition-contract.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0035](0035-node-agent-trust-and-local-protocol.md)
- [ADR 0036](0036-replacement-api-freeze.md)
