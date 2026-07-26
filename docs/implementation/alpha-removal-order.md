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

1. Establish a durable quiescence fence while the old admission and deny paths
   are healthy. Suspend every enumerated workload owner at its source, prevent
   new alpha-enrolled Pod creation with an independently managed fail-closed
   admission rule, and prove a representative recreation is rejected. Keep the
   fence through replacement verification; operator intent alone is not a
   sufficient fence.
2. Stop every enumerated protected workload while the old deny path is still
   installed. Keep unprotected workloads outside the target set.
3. Verify from the container runtime, not only Kubernetes status, that no
   protected application process or runnable sandbox remains. Recheck workload
   owners and prove the admission fence still rejects recreation. Abort if any
   protected process remains or the fence is not authoritative.
4. While the alpha cleanup controller is still present, withdraw provider
   mappings and gateway rules, release external leases safely, remove allocation
   and provider-lease quarantine finalizers, and verify every enumerated alpha
   object has no controller-owned finalizer. Never force-remove a provider
   finalizer before its bounded cleanup or explicit quarantine outcome.
5. Uninstall the remaining alpha runtime while retaining the quiescence fence,
   explicit target inventory, and a node/runtime quarantine capable of denying
   or terminating a recreated protected sandbox. Immediately repeat the runtime
   process, sandbox, owner, and fence checks. If anything protected reappears,
   keep or reinstall denial, terminate or isolate it, and abort before purge.
6. Present the exact alpha CR instances and four alpha CRDs again, require an
   explicit destructive confirmation, then delete those instances and CRDs.
   Refuse globs, an unresolved context, a changed cluster identity, or a target
   set that differs from the preflight inventory.
7. Verify alpha webhook configurations, controller workloads, injected
   components, ConfigMaps, finalizers, RBAC, and runtime processes are absent.
   Unknown residual alpha markers are a hard failure, never a reason to allow
   ordinary egress.
8. Install fresh replacement CRDs, chained CNI, node agent, controller, and the
   immutable default gateway class. Require every node selected for protected
   workloads to advertise and pass the supported CNI capability checks.
9. Apply newly authored gateway, route, workload, lease, and adapter manifests.
   Do not read alpha objects to populate them.
10. Restart protected workloads only after enrollment lookup, exact Pod UID
   binding, deny-first CNI programming, gateway programming, tunnel health, and
   protected DNS are observable.
11. Reacquire allocations and provider mappings as new state. Verify protected
   TCP, UDP, DNS/UDP, DNS/TCP, and fragmentation; tunnel-loss denial; and
   ordinary egress only for explicitly unprotected controls.

Any failure before step 10 leaves protected applications stopped behind the
quiescence fence. For workloads that have not started, any failure during or
after step 10 must leave creation-time denial installed or make CNI `ADD` fail.
For an already-running protected workload, tunnel, DNS, gateway, controller,
agent, startup, reconfiguration, or replacement failure must withdraw the
active allow path while the node-owned base deny remains. If withdrawal cannot
be observed, cordon and quarantine the node, then terminate or runtime-isolate
the exact protected sandboxes; never expose ordinary egress as recovery.
Verification must capture zero direct TCP, UDP, DNS/UDP, DNS/TCP, and fragmented
UDP packets both before and after withdrawal and must show `Ready` is no longer
True. Rollback does not restore alpha objects or use a sidecar path; it restores
an exact supported replacement artifact or keeps protected workloads stopped.

## Required drill evidence

Issue #139 owns the executable, confirmation-gated runbook and drill. It must
record a one-way fingerprint over the intended context, API server identity,
cluster UID and trust root without recording their raw values; a digest of the
canonical sorted preflight target set; target counts; runtime process absence;
purge confirmation; exact release identities; elapsed clean-install time;
protected/unprotected packet results; and failure-path outcomes. Evidence must
contain neither credentials nor private endpoints, and the same fingerprints
must be re-derived immediately before destructive confirmation.
