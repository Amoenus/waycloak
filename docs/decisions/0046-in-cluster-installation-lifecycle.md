# ADR 0046: In-cluster installation lifecycle

- Status: Proposed; artifact and application adapters implemented, reconciler pending
- Date: 2026-10-03
- Tracks: [#252](https://github.com/Amoenus/waycloak/issues/252)

## Context

ADR 0042 made the external CLI responsible for release transitions. ADR 0045
added clean GitOps bootstrap while retaining that upgrade boundary. This does
not satisfy the product requirement: an administrator must be able to update
the release version and applicable configuration in Kubernetes desired state
and have the cluster reconcile an upgrade, rollback, or interrupted operation.

The runtime chart cannot safely implement this by removing its immutable-class
guard. A normal apply can then replace agents, CNI, and controller resources
before the existing authenticated deny hold has completed. A Helm hook that
runs another upgrade of the same release also conflicts with the outer pending
revision. Neither approach transfers transition ownership into the cluster.

## Proposed decision

Introduce a separate installation controller and a cluster-scoped installation
resource. The installation chart installs that controller and desired state;
the controller alone applies the existing runtime chart through the Helm Go
library. Plain YAML and GitOps submit the same installation resource. Neither
the Helm executable nor `waycloakctl` is required in the controller image.

The new API is separate from the frozen networking API. Only cluster
administrators may change installation intent: it authorizes privileged node
components, admission policy, and cluster RBAC. The runtime chart remains an
implementation artifact, with its direct-upgrade guard intact until adoption
and ownership transfer are complete.

A version resolves to the project's published release manifest. Verification
requires the exact publication workflow, exact requested tag, GitHub OIDC
issuer, certificate transparency, artifact transparency, and a trusted signing
timestamp. The manifest selects the immutable chart and runtime images. The
controller must not accept arbitrary artifact URLs or signer policies from an
installation resource.

The shared transition engine retains the existing deny-first sequence:

1. Verify artifacts, source inventory, networking API compatibility, certificate
   identity, CNI layout, and node coverage before mutation.
2. Persist the complete target intent and source observation in an immutable
   journal, bound to installation UID and generation.
3. Establish the node hold and await authenticated denial for every attachment.
4. Replace the immutable class as needed and stage the target controller, CNI,
   and held agents.
5. Replace stale gateway Pods under the hold and await observed data-plane health.
6. Activate the target agents, verify the complete target, and complete the journal.

Configuration changes use this ownership boundary too. They must not bypass the
hold merely because the image version remains unchanged. Unsupported API or
storage downgrades remain rejected. Newer desired generations queue behind the
active immutable target; they cannot silently retarget an interrupted operation.

The installation controller needs its own leader-election Lease and durable
operation ownership. A pending Helm revision may be recovered only after proving
that it belongs to that installation's journal and that its previous executor
no longer holds the Lease. A foreign pending operation is an actionable failure,
not permission to rewrite Helm state.

## Adoption

Existing installations need a one-time declarative ownership transfer. Install
the lifecycle controller separately, retain the existing runtime release name
and namespace, and stop the previous manager from reconciling or pruning runtime
resources before adopting them. The migration must preserve certificates,
workload/storage identities, CNI paths, and node coverage. It must require no
Waycloak CLI and must have an acceptance test starting from v1.0.1.

After adoption, ordinary version/configuration changes affect only installation
desired state. No runtime dependency on Argo CD, Flux, or another GitOps product
is introduced. Exact migration manifests and conflict detection remain part of
the pending implementation; this ADR is not an operational migration recipe.

## Implemented boundary and outstanding acceptance

`InstallRuntime` separates the existing journal-bound transition engine from
shell execution. The optional CLI retains its Helm command adapter. The new
installation package contains signature verification and an in-process Helm
adapter with the same staged value overlays and server-side application mode.
Offline tests verify a real published signature, reject altered artifacts and
tag substitution, exercise staged application with an empty executable search
path, and refuse to take over pending Helm operations.

This foundation does not install a reconciler or complete #252. The installation
API/chart, generation-bound status, durable recovery at every intermediate
write, configuration-only reconciliation, adoption, and full packet-level
upgrade/rollback acceptance still need implementation. Until then, ADR 0042
and ADR 0045 remain the supported operational procedures. The running rc.3
deployment is unaffected by these library additions.

## Tradeoffs

The Helm and Sigstore libraries add a substantial dependency graph. The lifecycle
controller should be a separate binary so those imports do not become part of
the node agent or protected workloads. Its release needs the same vulnerability
and artifact-verification gates as the existing runtime. The initial dependency
review pins the corrected ORAS, gRPC, crypto, and module-tooling versions instead
of relying on the libraries' older transitive selections.
