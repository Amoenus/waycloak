# ADR 0031: CRD installation, clean cutover, and storage lifecycle

Status: Proposed
Date: 2026-07-26

## Context

CRDs are cluster-scoped APIs. Helm installs files in `crds/` before templates
but intentionally does not upgrade or delete them. Deleting a CRD deletes all
instances. Waycloak also persists UID-bound allocation and lease state whose
loss can cause incorrect recovery or delivery even when traffic remains closed.

The sole current user has authorized breaking changes. Carrying alpha versions
and a conversion webhook into the replacement architecture would add more risk
than an explicit maintenance cutover.

## Decision

The cluster installation lifecycle owns CRD declarations. The runtime controller
never creates, owns, upgrades, or deletes them.

The alpha-to-replacement transition is stop, purge, install, author and
verify—not migration. The release provides:

- a cutover plan that keeps old deny rules until protected workloads are stopped;
- a fresh signed CRD bundle and verification of discovery/schema identity;
- post-install protected/unprotected smoke tests; and
- a separately confirmed alpha CRD deletion step.

No alpha object is converted or imported. New UID-scoped allocations are created
during controlled workload restart. Port-forward mappings are reacquired from
new intent; reuse is unsafe until the new lease reports Ready.

After the replacement API reaches beta, ordinary upgrades are in-place and
non-breaking. Each release publishes one signed CRD bundle tied to the chart and
component digest manifest. Storage-version changes follow Kubernetes CRD
versioning rules: apply CRDs first, ensure every running controller understands
served/storage versions, migrate stored objects explicitly, audit
`status.storedVersions`, then remove obsolete serving only in a later release.
Semantic coexistence, if ever necessary after beta, requires a pure, lossless,
round-trip-tested conversion webhook with no Secret reads or external effects.

Uninstall and CRD purge remain separate commands. Normal uninstall leaves user
intent and CRDs. Purge requires enumeration, finalizer audit, export, explicit
confirmation and a warning that CR instances are unrecoverably deleted unless
restored from the export.

## Consequences

- No conversion infrastructure is carried merely for prototype compatibility.
- Alpha replacement requires downtime and regenerated allocation/lease state.
- Future stable upgrades have a conventional, tested Kubernetes API lifecycle.
- `helm upgrade` alone is never represented as a CRD migration mechanism.
- Backup, interrupted-upgrade and disaster-restore tests are release gates.

## Alternatives rejected

- Serve alpha and beta together indefinitely: retains two authorities and test
  matrices.
- Let the controller self-manage CRDs: unsafe API/runtime coupling.
- Delete/recreate CRDs while protected workloads run: incompatible with a
  fail-closed replacement.
- Promise lossless translation of controller-owned runtime state: old and new
  data planes do not share a trustworthy applied-state meaning.

## Related decisions

- [ADR 0025](0025-api-stability-and-feature-channels.md)
- [ADR 0032](0032-turnkey-bootstrap-and-preflight.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
