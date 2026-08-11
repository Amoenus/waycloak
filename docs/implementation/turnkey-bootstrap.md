# Turnkey bootstrap and verification

Status: implementation slice for issue #138; stable acceptance remains pending
Last updated: 2026-08-09

`waycloakctl` is a stateless assistant around the Helm and Kubernetes APIs. It
does not become a controller, store VPN credentials, translate alpha objects,
or weaken an unsupported cluster into a nominally successful install. JSON
outputs use `cli.waycloak.io/v1`.

## Artifact verification

Download one immutable `waycloakctl-<os>-<arch>` binary together with
`SHA256SUMS`, `SHA256SUMS.sigstore.json`, and `waycloakctl.spdx.json` from the
same release. Verify the checksum and keyless Sigstore bundle before running
the binary. The release workflow builds each supported binary twice, compares
the outputs, publishes an SPDX SBOM, and records GitHub build provenance.
Tags containing a SemVer prerelease suffix are published as GitHub
prereleases. A separate hosted-runner job downloads the published asset set and
rejects a missing or extra asset, checksum mismatch, unexpected Sigstore
workflow identity or issuer, non-tag source reference, wrong source commit, or
self-hosted provenance before the release run can pass.

An operator can independently repeat the important checks after downloading
one release into an empty directory:

```text
sha256sum --check SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/Amoenus/waycloak/.github/workflows/waycloakctl-release.yaml@refs/tags/<tag> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
cosign verify-blob --bundle waycloakctl.spdx.sigstore.json \
  --certificate-identity https://github.com/Amoenus/waycloak/.github/workflows/waycloakctl-release.yaml@refs/tags/<tag> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com waycloakctl.spdx.json
gh attestation verify waycloakctl-<os>-<arch> --repo Amoenus/waycloak \
  --signer-workflow Amoenus/waycloak/.github/workflows/waycloakctl-release.yaml \
  --source-ref refs/tags/<tag> --deny-self-hosted-runners
```

## Clean-install sequence

The first two commands are read-only. Keep the generated plan as the reviewed
change record.

```text
waycloakctl preflight --context <context> --overlay-cidr <reviewed-cidr> --output human
waycloakctl install plan --context <context> --release-manifest <verified-release.json> \
  --node-architecture <amd64-or-arm64> >install-plan.json
waycloakctl install apply --context <context> --plan install-plan.json --confirm <planID>
```

The architecture flag is optional only when preflight observes exactly one
architecture. A mixed-architecture cluster requires an explicit reviewed row.
The generated values constrain both the CNI installer and node agent to that
architecture; only those nodes can publish the exact `cni-ready` capability
that enrolled workloads select. Building a multi-platform image is not treated
as conformance evidence for an otherwise unproved node row.

The release manifest must satisfy
[`release-manifest-v1.schema.json`](../api/release-manifest-v1.schema.json) and
name the exact chart, controller, CNI, node-agent, gateway-agent, Gluetun, and
pause digests. Its `manifestDigest` is verified against canonical JSON over
every identity field except the digest itself; profile order and insignificant
file formatting do not change that identity, while any artifact or version
change does. Extra image entries are rejected. `install plan` repeats preflight
and refuses an incompatible cluster. The plan lists namespace privilege, host
CNI paths, exact Helm values,
Secret object names, rollback, and purge boundaries. It never contains Secret
data. Preflight hashes the trusted server/CA/cluster identity, Kubernetes and
exact runtime versions, architectures, kernels, operating systems, primary-CNI
identity, network observations, and check results. The install plan binds that
observation digest, overlay, and selected architecture into its identity.
`install apply` accepts only the exact recalculated plan ID, re-runs the same
preflight before any Namespace or Secret creation, and refuses changed or
incompatible observations. It then creates the
observation CA and serving key in memory, stores them directly as Kubernetes
Secrets, and never passes private key material through Helm.

On a clean cluster, the reviewed apply is deliberately staged. The first Helm
revision installs the CRDs and starts the controller while the CNI installer,
node agent, and default class remain disabled. After that revision is Ready, a
second revision activates the exact reviewed baseline runtime. This prevents the
new chained CNI from becoming authoritative before the ordinary-networked
controller exists. New Pod sandboxes may fail closed during baseline activation;
there is no namespace bypass or fail-open interval. A same-manifest re-apply
goes directly to the full reviewed revision. A changed deployed release never
uses the clean-install bootstrap: its first transition revision deploys the
target controller, CNI installer, and class while retaining the exact reviewed
source node-agent image and release identity. After that revision is Ready and
the target CNI receipt exists, a second revision activates the target node
agent. The old agent socket therefore remains available while the installer
changes, and the installed deny path is never removed or bypassed.
The installer DaemonSet is independently host-networked and tokenless, so its
own sandbox never depends on the chained plugin or local socket it owns. This
also makes an interrupted or externally orchestrated installer/agent restart
recoverable without creating an ordinary-egress path; enrolled workloads still
fail CNI `ADD` while the local authority is unavailable.
The target agent validates the exact receipt, restores lockdown, completes a
fresh authenticated controller-relay handshake, reconciles retained attachment
state, and only then republishes node capability. Public CNI operations remain
gated throughout; the recovery reconciler alone bypasses that readiness gate to
avoid a backend-readiness dependency cycle.

Disruptive verification deletes its exact probe Pods before its route, waits for
each Pod and UID-derived binding to be absent, and only then removes and observes
the route. `cleanupComplete=true` therefore means finalizer-backed data-plane
withdrawal finished; accepted deletion requests or terminating bindings are not
reported as complete.
Failed-`ADD` cleanup does not depend on catching a brief terminating-Pod API
window. The authenticated CNI/node protocol returns a distinct exact-Pod-absent
result, then binds withdrawal to the durable sandbox/interface/netns identity
and publishes zero applied state before discarding that record. API ambiguity,
agent loss, and foreign netns reuse retain denial and never claim cleanup.
The same ordering covers kubelet's failed-`ADD` behavior: an early `DEL` cannot
discard sticky enrollment while the Pod remains pending, and periodic recovery
completes withdrawal after exact Pod absence without replaying an already
accepted observation after its binding is gone.

Every plan also binds the exact currently deployed Helm revision, release
manifest, six runtime images, six CRD specifications, default gateway-class
UID/generation, and observation-certificate UIDs and public digests. Apply
re-observes that source and re-reads the target chart before mutation. The
initial beta lifecycle permits a transition only when the target has the
identical served/storage CRD contract; any schema or storage change requires a
separately reviewed storage-migration procedure. Forward transition and
rollback use the same target-bound plan/apply path. A rollback therefore names
a separately verified prior release manifest and never delegates identity to
an opaque Helm revision number. Ordinary transitions replace the exact old
immutable gateway-class UID at its stable name, preserve observation trust
identity, execute the two-revision CNI/agent transition, and verify the exact
target runtime after Helm completes. The class replacement and transition
windows remain fail closed behind the installed CNI deny path.
Connected Helm rendering refuses a changed live class identity before any
release mutation. Argo CD's offline render cannot perform that lookup, so the
class is assigned an earlier sync wave and a direct sync stops on immutable
class application before runtime components advance. For GitOps, commit the
reviewed target while automatic runtime sync is suspended, execute the exact
`waycloakctl install plan/apply` transition, and only then sync Argo to confirm
the already matching target. Do not delete the class manually or use Argo as
the transition executor.
Every lifecycle Helm mutation uses explicit server-side apply and
`--force-conflicts`. The reviewed plan therefore authorizes Waycloak's Helm
field manager to reclaim only fields rendered by that exact chart and values
from another server-side manager. It does not use Helm's broad
`--take-ownership` annotation override. GitOps controllers may retain and
observe the release declaration, but must not race `waycloakctl` by applying
the rendered runtime objects during a lifecycle transaction.
Before the first destructive transition action, apply persists the complete
non-sensitive reviewed plan in one immutable release-scoped lifecycle journal.
The same plan may resume only an exact class-withdrawn, target-class with exact
source runtime, source-agent-retained staging, or completed-target checkpoint.
Planning returns the journaled plan only for the same verified target and
preflight; arbitrary skew, a different target, or a missing/foreign journal
remains refused.

If that exact transition leaves one newer Helm Secret in `pending-upgrade` or
`failed`, ordinary install and certificate operations remain blocked. Repair is
an explicit reviewed transaction:

```text
waycloakctl install repair plan --context <context> --namespace <namespace> \
  --release <release> --output json >install-repair.json
waycloakctl install repair apply --context <context> --plan install-repair.json \
  --confirm <repair-planID>
```

The repair plan binds the original transition, current preflight, exact source
deployed revision, and the sole newer stuck Secret's name, UID, type, status,
version and full-object digest. Opaque Helm data is hashed but never copied into
the plan or journal. Apply creates an immutable repair journal, deletes only the
exact stuck UID, and resumes the original class-withdrawn, staged, or target
checkpoint. A partial server-side apply that recreated the exact target class
while leaving every executable component at the exact source identity is a
separate supported checkpoint and re-enters staging with the source node agent
held. Repair does not relabel or rewrite Helm storage and does not call an
unverified rollback. Interrupted deletion and post-Helm cleanup are retryable;
revision or runtime ambiguity is a hard stop.

The node agent resolves each CNI request's exact Pod UID and node assignment
with a direct API-server read. Its informer cache remains useful for ordinary
reconciliation, but it is not authoritative for creation-time identity or Pod
name reuse. The direct reader uses a distinct projected token with the API
server's default audience; the audience-bound observation token is isolated and
cannot be used as a portable Kubernetes API credential.

Release automation, never the cluster operator, assembles this input with the
publisher-only `go run ./hack/release` command. The command requires an
exact OCI chart identity and exactly the replacement controller, CNI, node
agent, gateway agent, gateway runtime, qBittorrent adapter, Gluetun, and pause
image identities. It performs no tag
resolution or registry discovery and rejects missing, extra, duplicate, or
mutable inputs before emitting deterministic JSON. The resulting manifest is
then signed and published by the release lifecycle; installation consumes that
verified file without requiring source or image-digest knowledge.

CI also constructs a disposable Kind cluster with a job-local OCI registry and
uses the real CLI boundary for preflight, plan, and apply. The acceptance first
proves that an incorrect confirmation creates no namespace, then verifies the
exact chart and runtime image identities, release-bound CNI receipt and chain,
authenticated node capability, default class identity, and healthy doctor
output. It then builds exact disposable fixture artifacts, creates runtime-only
WireGuard keys and TLS, and runs the confirmation-gated disruptive verification
against one HTTPS observer reached through ordinary and protected paths. The
gate proves refusal without mutation, distinct observed source addresses,
exact-UID gateway replacement, protected application startup denial during the
loss window, ordinary-network continuity, recovery, and exact cleanup. The
fixture is CI evidence for the baseline fail-closed mechanics, not a supported VPN provider and
not a substitute for the signed published-artifact or real-provider gates.

The same exact-artifact journey exercises the first disaster-recovery slice.
It exports deterministic portable gateway/route intent, deletes both live
objects, proves an enrolled Pod receives no application container while its
route is missing, rejects a wrong restore confirmation without mutation, then
restores with the exact plan. Recovery must create a new gateway UID, reacquire
a new exact Pod-UID binding, and succeed without importing old status or runtime
state. This logical drill complements, but does not replace, coherent
distribution datastore-snapshot certification.

The Kind lifecycle gate builds two immutable chart/release identities with an
identical CRD contract. It installs the baseline, performs a source-bound
forward transition, and then applies a separately planned rollback to the
baseline. Both directions require a newer Helm revision, unchanged observation
certificate UIDs and public bytes, a new immutable gateway-class UID with the
exact target release identity, exact runtime arguments and CNI receipt, healthy
capability observation, and the complete
gateway-replacement fail-closed exercise. In each direction the gate terminates
the real CLI process immediately after the staging Helm revision completes,
requires an immutable exact-plan journal and degraded doctor report, proves an
enrolled application container never starts and receives no Pod IP, re-plans to
the identical plan ID, and resumes without repeating staging. This proves the
supported identical-schema beta row and exact staged-interruption recovery;
the same gate now uses admission fault injection to stop an explicit observation
certificate rotation after overlap and after serving-key switch. At both
checkpoints the immutable plan/journal recovers exactly, held agents prove an
authenticated TLS observation without publishing CNI-ready scheduling, and an
enrolled Pod receives no container or Pod IP. Completion preserves stable
Secret UIDs, removes old trust and staged private state, and carries the new
rotation identity into later release plans. Distribution snapshots remain
separate issue #32 evidence.

Normal Helm uninstall intentionally does not restore the primary CNI chain or
delete CRDs. Those are separate destructive operations covered by issue #139.
An alpha API causes `preflight` to fail: stop protected workloads and complete
the explicit alpha purge procedure before installing the replacement. There is
no migration or conversion path.

For an existing alpha installation, use the separately reviewed
[destructive purge runbook](../operations/alpha-purge-and-reinstall.md).
`waycloakctl alpha-purge plan` is read-only; `alpha-purge apply` binds deletion
to exact cluster/CR/CRD UID fingerprints and requires explicit runtime-empty and
separate-uninstall attestations.

Portable backup and restore use the separately documented
[disaster-recovery procedure](../operations/backup-restore-and-disaster-recovery.md).
They do not back up Secrets, ConfigMaps, workloads, bindings, allocations,
provider mappings, or live observations, and they never perform API conversion.

## Gateway and workload verification

Create the provider credential Secret separately, then render non-secret native
configuration and a `VPNGateway` that only references it:

```text
waycloakctl gateway init --namespace <namespace> --name <gateway> \
  --config-map <config-map> --secret <credential-secret> \
  --overlay-cidr <reviewed-cidr> >gateway.yaml
kubectl apply -f gateway.yaml
waycloakctl doctor --namespace <namespace> --output human
```

The initial reviewed recipe is Proton/OpenVPN through Gluetun. The application
Pod receives only the `networking.waycloak.io/egress-route` label; it receives
no Waycloak sidecar, init container, capability, host mount, VPN credential, or
Kubernetes credential.

`waycloakctl verify` is deliberately disruptive. It requires a gateway rendered
with `gateway init --allow-disruptive-verify`, an immutable Waycloak probe image,
and the exact confirmation digest printed by a refused unconfirmed invocation.
The probe receives an HTTPS observer URL and, when needed, a same-namespace
ConfigMap containing only public `ca.crt`; both names are bound into the
confirmation digest. It receives no service-account token or Secret. The command
creates only run-labeled route/probe objects, proves ordinary and protected
egress differ, deletes the exact UID-owned gateway Pod, proves new protected
application containers do not start during loss while ordinary networking
continues, waits for a current-generation Ready UID-bound recovery binding, and
deletes only its own objects. The report exposes both the outage-denial and
recovery-binding observations so lifecycle certification does not infer them
from a later successful request.

## Diagnostics and support bundle

`waycloakctl doctor` reports resource identity, generation, allowlisted
conditions, and authenticated node capability counts. It omits condition
messages, addresses, endpoints, object specs, Secret data, and ConfigMap data.
The command reads the installed CNI-installer and node-agent DaemonSet selectors
and requires them to identify one installation with the same selected nodes.
Nodes outside an explicit reviewed architecture row are counted as
`NotSelected`; every selected node must retain the current authenticated
`cni-ready` capability. Missing or inconsistent components and selectors that
match no node are unhealthy rather than silently widening or narrowing the
diagnostic scope.

```text
waycloakctl support-bundle --context <context> --file waycloak-support-bundle.tar.gz
```

The deterministic, mode `0600` bundle contains preflight, doctor, and reduced
event summaries plus per-section digests. It excludes raw event messages,
logs, addresses, endpoints, credentials, keys, Secret objects/data, and
ConfigMap data. Review it before sharing.

## Current evidence and remaining acceptance

Unit tests cover unsupported clusters, alpha refusal, overlapping networks,
plan tampering, exact confirmation, certificate isolation, credential canary
redaction, deterministic bundles, and exact UID-scoped gateway disruption. A
privileged network-namespace test proves the gateway deny-first path, healthy
TCP/UDP forwarding, and tunnel-loss denial. The mandatory chained-CNI installer
is built twice as a Linux amd64/arm64 OCI layout and compared byte-for-byte in
CI. A release-wide composite OCI build target combines the four baseline runtime
images with the two port-forward implementation images, and deterministic manifest
assembly is exercised as a command-line boundary in CI. Every release carries
the complete inventory; this does not advertise or activate port forwarding.
Missing, partial, or unknown image inventories are rejected, and supported
rollback uses another complete signed release rather than a preview-manifest
compatibility path.

The `v0.0.0-turnkey.1` prerelease executed the signed CLI workflow from exact
main commit `21ffebea3444f830ec2c9b29acebd9b36a2fd878`. Release run
`30360505871` passed publication and the separate hosted-runner verification of
the complete downloaded asset set.

Waycloak beta candidates use `vMAJOR.MINOR.PATCH-beta.NUMBER` tags and stable
releases use `vMAJOR.MINOR.PATCH`. The one release workflow repeat-builds the
six Waycloak amd64/arm64 binaries, packages the
chart with the tag-derived immutable version, publishes only digest-resolved
identities, and assembles `release-manifest.json` with the derived Gluetun
and pinned pause identities. The Gluetun image is built from upstream commit
`7eed6eaf160440724a93ca66f66055068cebe4ac` on upstream multi-platform image
digest `sha256:e3272b29a4bc177b389fbdcb54cf9716ccbfc30f04d8b7a35b0a5be9cdb58461`.
Only the reachable fixed Go dependencies are advanced; the release includes
the exact patch, binary checksums, and preserved upstream MIT license.
Call-graph analysis, upstream privileged tests, and the image vulnerability
scan must pass. Publication is refused on HIGH/CRITICAL fixed vulnerabilities.
Every published OCI artifact receives an SPDX attestation, keyless signature,
and GitHub provenance; release files receive signed checksums and provenance. A
separate hosted runner redownloads the release and
verifies the exact workflow identity, issuer, source tag/commit, platform
indexes, chart bytes/CRDs, manifest-to-registry equality, and all signatures and
attestations. Shipping the gateway runtime and qBittorrent adapter only makes
their immutable artifacts available for explicitly enabled port-forward tests; it
does not add the optional feature to a default class or claim its conformance.
The workflow is not evidence until an exact tag run passes.

Issue #138 must remain open until the published Waycloak candidate completes the
supported clean-cluster Proton/OpenVPN journey in under 15 minutes. The
exact-artifact Kind installation and disruptive fixture coverage do not replace
that provider proof.
