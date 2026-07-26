# Alpha removal order

Status: reviewed input to issue #139; execution is intentionally blocked until
the replacement Core path required by issue #135 passes conformance.

The alpha system is teardown input and as-built evidence only. Nothing in this
sequence converts an object, carries runtime state forward, or creates a mixed
old/new serving period. Normal uninstall must never delete CRDs or user custom
resources. Destructive purge is a separate, explicitly confirmed operation.

## Preconditions

1. Resolve the exact target cluster and record its server identity and context.
2. Enumerate every alpha `VPNGateway`, `VPNWorkload`, `PortForwardLease`, and
   `WorkloadAdapter`, plus every Pod template carrying an alpha Waycloak marker.
3. Save only non-secret names, namespaces, UIDs, owners, and counts needed to
   prove the target set. Do not export Secrets, provider endpoints, allocations,
   leases, or runtime observations for reuse.
4. Prepare new replacement manifests independently, but do not start replacement
   workloads or install replacement CRDs yet.

## Fail-closed destructive sequence

1. Stop every enumerated protected workload while the old deny path is still
   installed. Keep unprotected workloads outside the target set.
2. Verify from the runtime, not only Kubernetes status, that no protected
   application process or runnable sandbox remains. Abort if any remains.
3. Uninstall the alpha runtime while retaining the explicit target inventory.
4. Present the exact alpha CR instances and four alpha CRDs again, require an
   explicit destructive confirmation, then delete those instances and CRDs.
   Refuse globs, an unresolved context, a changed cluster identity, or a target
   set that differs from the preflight inventory.
5. Verify alpha webhook configurations, controller workloads, injected
   components, ConfigMaps, finalizers, RBAC, and runtime processes are absent.
   Unknown residual alpha markers are a hard failure, never a reason to allow
   ordinary egress.
6. Install fresh replacement CRDs, chained CNI, node agent, controller, and the
   immutable default gateway class. Require every node selected for protected
   workloads to advertise and pass the supported CNI capability checks.
7. Apply newly authored gateway, route, workload, lease, and adapter manifests.
   Do not read alpha objects to populate them.
8. Restart protected workloads only after enrollment lookup, exact Pod UID
   binding, deny-first CNI programming, gateway programming, tunnel health, and
   protected DNS are observable.
9. Reacquire allocations and provider mappings as new state. Verify protected
   TCP, UDP, DNS/UDP, DNS/TCP, and fragmentation; tunnel-loss denial; and
   ordinary egress only for explicitly unprotected controls.

Any failure before step 8 leaves protected applications stopped. Any failure
during or after step 8 must leave creation-time denial installed or make CNI
`ADD` fail so the application cannot start. Rollback does not restore alpha
objects or use a sidecar path; it restores an exact supported replacement
artifact or keeps protected workloads stopped.

## Required drill evidence

Issue #139 owns the executable, confirmation-gated runbook and drill. It must
record target counts, runtime process absence, purge confirmation, exact release
identities, elapsed clean-install time, protected/unprotected packet results,
and failure-path outcomes without recording credentials or private endpoints.
