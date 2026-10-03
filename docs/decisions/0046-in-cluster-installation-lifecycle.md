# ADR 0046: In-cluster installation lifecycle

- Status: Implemented for qualification; release and live acceptance pending
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

## Decision

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

Legacy Flannel adoption moves the selected in-place chain to
`05-waycloak.conflist`. Under the authenticated hold, the installer validates
that the preserved primary reproduces the legacy chain, durably writes the owned
chain, and only then restores the unchained upstream file. Directory entries are
synced on Linux before advancing. Interrupted filesystem prefixes retain a
selected guard and are replayable. Existing owned layouts and node selectors
remain unchanged; an unexplained primary topology change is rejected.

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
is introduced. The immutable runtime ownership record rejects external CLI
transitions after adoption. Journals owned by another installation UID are
rejected. Live adoption and packet-level upgrade qualification remain gates.

## Implemented boundary and outstanding acceptance

`InstallRuntime` separates the existing journal-bound transition engine from
shell execution. The optional CLI retains its Helm command adapter. The new
installation package contains signature verification and an in-process Helm
adapter with the same staged value overlays and server-side application mode.
Offline tests verify a real published signature, reject altered artifacts and
tag substitution, exercise staged application with an empty executable search
path, and refuse to take over pending Helm operations.

The installation API, separate Helm chart, controller process, immutable journal,
generation-bound status, and configuration transitions are implemented. Tests
restart the reconciler at each phase, queue superseding intent, reject foreign
UIDs and pending Helm operations, and withdraw readiness after observed health
loss. API-server tests enforce singleton ownership and immutable network identity.
These tests do not complete #252: release publication, v1.0.1 adoption, and live
packet-level upgrade, rollback, and infrastructure recovery remain acceptance
gates. Until qualification completes, the running rc.3 deployment continues to
use ADR 0042 and ADR 0045.

## Tradeoffs

The Helm and Sigstore libraries add a substantial dependency graph. A separate
Deployment invokes the installation subcommand in the signed replacement-controller
image, preserving the existing nine-image release contract. This increases the
networking controller binary; it does not add these imports to node agents or
protected workloads. The installation process has separate resource limits and
host networking so CNI startup failure cannot prevent recovery. Its release
needs the same vulnerability
and artifact-verification gates as the existing runtime. The initial dependency
review pins the corrected ORAS, gRPC, crypto, and module-tooling versions instead
of relying on the libraries' older transitive selections.

The stripped Linux amd64 controller binary measured 68,042,914 bytes with
`CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and `-ldflags '-s -w'` during
implementation. This is a binary-size observation, not a resident-memory result.
The existing dependency-refresh budget records describe an earlier baseline;
live process memory and both architectures remain qualification requirements.
The local `wait-ready` helper reached 48,008 KiB maximum RSS during a two-second
unavailable-endpoint check. Its previous 32 MiB limit was insufficient; helper
limits are now 96 MiB. This startup measurement does not replace live controller
memory qualification.

Both controllers use host networking so an unavailable CNI agent cannot prevent
their replacement Pods from starting. The runtime Deployment uses a rolling
update with zero surge to release its host listener ports before replacement.
Keeping its strategy type also permits server-side adoption of older defaulted
Deployments. Its observation listener
still authenticates node reports. The installation controller uses node DNS for
public release verification. The CNI installer waits on the controller Service
address injected by kubelet, rather than requiring cluster DNS during recovery.
Runtime controller health and enabled metrics listeners are consequently exposed
on the selected host; operators must reserve their configured ports.
