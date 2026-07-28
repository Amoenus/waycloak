# Destructive alpha purge and clean replacement reinstall

Status: executable planning/purge slice implemented; destructive drill pending
Last updated: 2026-07-28

This is a one-way maintenance procedure, not migration. Do not translate,
export/import, dual-serve, or reuse alpha allocations, leases, endpoints, or
runtime observations. Normal Helm uninstall and destructive CR/CRD purge are
separate operations.

## 1. Read-only inventory

Run from an independently trusted workstation with an exact kubeconfig context:

```text
waycloakctl alpha-purge plan --context <context> --output json >alpha-purge-plan.json
```

The plan records only hashed server/trust/cluster identities; exact CR/CRD,
workload-owner, and Pod metadata; counts; and canonical digests. It does not
record object specs/status, Secret/ConfigMap data, provider endpoints,
allocations, lease values, or credentials. Review every target. Custom workload
controllers outside Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, and
CronJob require an additional operator-owned inventory and stop procedure.

Abort if the context is unresolved, any fingerprint is unavailable, an unknown
alpha target exists, or the planned set is incomplete. Store the exact old
release artifacts and non-secret source manifests independently for emergency
reinstallation; they are not inputs to the replacement.

## 2. Establish and prove quiescence

While the old deny/admission path is healthy:

1. Install an independently managed admission fence that rejects creation of
   every planned protected workload and prove a representative rejection.
2. Suspend each planned workload owner at its source and stop every protected
   Pod. Do not stop unrelated unprotected controls.
3. Inspect the container runtime on every relevant node and prove no protected
   application process or runnable sandbox remains. Kubernetes Pod absence
   alone is insufficient.
4. Keep packet capture active at the node/direct-egress boundary and record zero
   direct TCP, UDP, DNS UDP/TCP, and fragmented UDP packets.

If any process, sandbox, or direct packet remains, keep the old deny path,
isolate the exact node/workload, and abort.

## 3. Clean alpha external state and uninstall runtime

With the alpha cleanup controller still running, withdraw provider mappings and
gateway rules, release external leases, and wait for bounded cleanup or explicit
quarantine. Every planned alpha CR must have zero controller finalizers.

Uninstall the alpha Helm/runtime release separately. Do not delete CRDs. Recheck
the independent fence, workload owners, Kubernetes Pods, container-runtime
processes/sandboxes, and direct-egress capture. Abort if anything protected
reappears.

## 4. Explicit CR and CRD purge

Rerun the read-only plan and compare cluster and target identities with the
reviewed plan. The apply command re-derives them again, permits only already
deleted subsets for idempotent recovery, and rejects additions, UID reuse,
remaining protected Pods, or remaining CR finalizers.

```text
waycloakctl alpha-purge apply --context <context> \
  --plan alpha-purge-plan.json \
  --confirm <exact-planID> \
  --attest-runtime-empty protected-runtime-empty \
  --attest-alpha-uninstalled alpha-runtime-uninstalled
```

The attestations assert that a human/operator-owned runtime inspection and the
separate alpha uninstall were completed; the CLI does not pretend Kubernetes
status proves either. Purge uses exact UID preconditions, deletes enumerated CR
instances before their four CRDs, waits for an empty target set, and emits a
non-sensitive report. Deletion is unrecoverable from Waycloak.

## 5. Clean replacement install

Keep the fence active. Verify discovery no longer serves alpha, then use the
signed exact-artifact workflow in
[`turnkey-bootstrap.md`](../implementation/turnkey-bootstrap.md): preflight,
reviewed install plan, confirmation-gated apply, fresh gateway recipe, and
doctor. Author new route/workload/lease/adapter manifests from source intent,
not the purge plan or old objects.

Restart protected workloads only after every selected node reports exact Core
capability and the new gateway reports current `Accepted`, `ResolvedRefs`,
`Programmed`, and live `Ready`. New Pod UIDs must receive new bindings,
allocations, and provider mappings.

## 6. Verify and remove the fence

Run protected and unprotected controls plus confirmation-gated gateway-loss
verification. Require protected TCP/UDP/DNS/fragmentation through the selected
VPN, zero direct packets during tunnel/gateway/controller/agent loss, no stale
`Ready`, and ordinary egress only for the unprotected control. Remove the
independent fence only after the exact evidence is archived.

Before purge, recovery is to repair/reinstall the exact old deny runtime and
remain quiesced. After purge, the preferred recovery is an exact supported
replacement reinstall. Emergency alpha reinstallation uses only independently
backed-up exact artifacts and source manifests, keeps protected workloads
stopped, and never imports allocations or leases.
