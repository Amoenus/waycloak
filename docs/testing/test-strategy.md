# Test strategy

Networking claims require packet-level evidence. Unit tests and “Pod Ready” are insufficient.

## Test layers

### Unit tests

- annotation parsing and mutation idempotence;
- stable allocation and quarantine;
- exact UID-derived replacement binding identity, atomic Lease collision,
  exhaustion, restart recovery, stale generation rejection, and durable
  quarantine recreation when the active reservation is missing;
- condition transitions and observed generations;
- provider capability decisions;
- native engine ConfigMap precedence, reserved-key redaction, mount isolation,
  and deterministic rollout digests;
- lease renewal state machine;
- redaction and error classification;
- nftables/netlink desired-state calculation using fakes.

Run formatting, `go vet`, staticcheck, race tests, and fuzz tests for annotation/config parsing.

### Change-sensitive CI

Documentation-only changes use a lightweight gate for Markdown lint, local
links, fenced examples, and changed-file secret scanning. They do not require
the Go/race/envtest/Kind pipeline. Any change to workflows, source, API,
generated manifests, Helm, KCL, build/generation scripts, tests, or dependency
metadata runs the full pipeline. Mixed changes run both relevant layers.

An always-present aggregate check reports classification and selected-job
results so conditional execution cannot weaken branch protection. Unknown
diff bases and workflow-classification changes fail safe to the full pipeline.
Implementation is tracked by issue #64.

### Controller integration tests

Use envtest for:

- CRD validation/defaulting;
- reconciliation and ownership;
- controller restart with persisted resources;
- namespace authorization;
- finalizer behavior;
- status conflicts and retries.
- exact controller-name ownership, immutable release/profile identity, and
  semantic no-op suppression for classes and gateways;
- missing, foreign, deleting, mismatched, unsupported-feature and unauthorized
  gateway references with `Programmed=False` and no addresses;
- credential canaries absent from class and gateway status.

### Cluster end-to-end tests

Stable admission and scheduling acceptance additionally requires Kubernetes
1.36 policy compilation, mutation that preserves user selectors, no mutation
for unlabeled Pods, rejection of host-namespace and direct-node bypass,
`Unschedulable` status on nodes without authenticated CNI readiness,
foreign-node and release-skew report rejection, 20-second label expiry, and CNI
refusal when admission or a readiness label is bypassed. Tests must assert that
these paths start no application container and observe no ordinary-egress
packet.

The node agent must also reject missing, writable, symlinked, release-skewed,
tampered, or incorrectly chained installation receipts/artifacts, restore
lockdown, and withhold its positive capability report.

Use Kind for every pull request where practical and k3d/k3s in scheduled/release validation. Tests should deploy an isolated fake egress gateway before requiring external VPN credentials.

Mandatory scenarios:

1. an unlabeled Pod is not enrolled or mutated and reaches the normal egress
   observer;
2. a labeled Pod receives no Waycloak container, has an exact UID-bound
   binding, and reaches a different gateway egress observer;
3. an enrolled Pod cannot reach the internet before exact binding and overlay
   readiness;
4. deleting the gateway blocks egress without exposing normal node IP;
5. terminating the tunnel blocks egress;
6. restarting controller leaves data-plane protection intact;
7. agent repairs deleted owned routes/rules;
8. DNS service discovery and external resolution follow configured policy;
9. controller or admission loss does not affect unlabeled Pods and prevents a
   newly enrolled Pod from becoming runnable;
10. a labeled Pod that bypasses scheduling admission is rejected by CNI on an
    incapable node;
11. adding/removing members does not renumber allocations;
12. unrelated nftables rules survive agent setup and cleanup;
13. no provider Secret or ServiceAccount token appears in an application container.
14. engine-native ConfigMap changes reconcile, reserved conflicts fail
    `Accepted`, and no Secret file mount appears outside the engine container;
15. alpha CRDs, annotations, fields, runtime objects, and stored state are
    rejected or absent; no dual-serving or compatibility path exists.
16. a verified manifest input renders exactly one default Gluetun class while
    absent or malformed identity renders none or fails; a minimal gateway needs
    no Waycloak image digest;
17. class deletion, foreign-controller claims, unsupported features, and
    credential-reference loss produce stable conditions before any data-plane
    object or address is published.
18. a Gluetun-like priority-99 overlay policy route and default-drop
    `INPUT`/`FORWARD`/`OUTPUT` chains are present in the privileged gateway fixture;
    Waycloak must win the overlay return path, admit only its health/DNS and
    overlay-to-tunnel traffic, preserve handles on a no-op reconcile, remove
    permission on tunnel loss, and repair an engine firewall reset before
    readiness returns.
19. an enrolled libcurl asynchronous-resolver Pod receives exactly `ndots:1`,
    resolves the fully qualified Kubernetes service name through the reviewed
    cluster DNS path, and resolves an external name only through the engine and
    disposable tunnel. Proxy tests concurrently exercise A/AAAA, UDP and TCP,
    EDNS0, responses larger than a typical MTU, UDP truncation with TCP retry,
    search-expanded cluster-name containment, malformed replies and unavailable
    upstreams. Any failed active probe must withdraw `DNSReady`, composite
    `Ready`, and the protected allow path without direct DNS fallback.
20. gateway deletion or replacement immediately enqueues every affected Pod and
    binding. A fresh node observation alone must not retain
    `VPNWorkloadBinding Ready=True` after the exact referenced gateway becomes
    non-ready, is deleted, changes UID, or advances generation. Every outbound
    attempt after an exact gateway UID or generation change must fail until the
    replacement gateway and node path are both observed current. A same-UID
    gateway-Pod replacement sample may only fail or match the concurrently
    observed current VPN egress, never an earlier endpoint or ordinary egress;
    binding readiness returns only after the exact gateway and node path are
    both observed current.

### Port-forward tests

- typed same-namespace Service and named/numeric port resolution through an
  exact Service UID, controller-owned EndpointSlice, Pod UID/address, and
  current UID-bound `VPNWorkloadBinding`;
- deterministic sticky `SingleActive` selection with overlapping rollout
  endpoints, endpoint loss, rapid replacement, Pod name/UID/IP reuse, and
  withdraw-before-successor ordering;
- protocol-faithful provider acquisition, paired TCP/UDP capability checks,
  rotation, renewal, expiration, release, timeout, capacity regression, and
  provider-result failures;
- Gluetun-native configuration selects an explicitly supported engine
  capability before a port-forward runtime is rendered; incompatible provider
  or tunnel-mode combinations fail before mutation, and the generic runtime has
  no provider-specific command-line contract;
- durable collision-free provider internal-port allocation, restart/restore
  recovery, deletion quarantine, and no reuse while an old mapping can live;
- exact TLS 1.3 controller-to-gateway runtime identity, strict versioned
  messages, exact gateway UID, oversized/unknown input rejection, and no
  Kubernetes credential in the runtime;
- deterministic Helm rendering proves the baseline contains no port-forward container,
  mount, flag, Service, or feature advertisement; complete port-forward input
  renders exact digest and named mTLS identity, while partial or adapter-only
  input fails before installation;
- gateway provisioning creates the tokenless runtime and deterministic Service
  only for explicit SingleActive intent plus the supported TLS reference, and
  refuses to adopt or delete a foreign same-name Service;
- privileged TCP and UDP packet delivery to only the selected overlay address,
  provider-port return symmetry, atomic generation handoff, unmatched-tunnel
  drop, withdrawal, drift, and runtime restart;
- separate provider, gateway-rule, delivery, and adapter-acknowledgement
  observations with current generations, stale-observation rejection, and
  no-op status stability;
- stable Service/backend ports work without a `WorkloadAdapter`; artifact
  presence or generic protocol enablement cannot select an application
  integration;
- cross-namespace gateway consent and indistinguishable missing/unauthorized
  references; backend Services remain same-namespace;
- qBittorrent compatibility requires an immutable
  `ProviderAssignedApplicationPort` adapter capability, exact EndpointSlice Pod
  address, application-owned HTTPS, listener update/readback/probe, all-torrent
  reannounce, durable restart revalidation, and backend-port restoration on
  withdrawal;
- Kind/k3d rollout tests and real-provider qBittorrent tests prove no wrong-Pod
  delivery, stale advertisement, or direct-egress fallback. Failure keeps this
  optional capability unavailable.

### Adapter conformance

Every workload adapter image must pass the language-neutral black-box suite
for current, rotated, expired, missing, duplicate, wrong-UID, and stale
generations. The suite verifies exact acknowledgement, bounded retry,
least-privilege execution, and readiness regression without direct-egress
fallback. The qBitTorrent adapter is the reference implementation, not a
special baseline code path.
Before adding an application adapter, compatibility evidence must also prove
that fixed-port translation, a standard application protocol, and the neutral
lease record cannot satisfy the application contract.

### Data-plane backend conformance

Every supported backend passes the same packet-level startup, loss, drift,
restart, detach, upgrade, and cleanup suite. Backend-specific tests supplement
but never replace those assertions. eBPF evaluation must record actual kernel
features, BTF, architecture, CNI, hooks, and verifier results; kernel version
alone is insufficient evidence.

### Stable CNI and node-agent abuse conformance

Every supported CNI/runtime row runs the creation-time proof with the
authenticated `cni-node/v1` channel. Unit/abuse coverage rejects non-root Unix
peers, missing or foreign keys, unsafe key/directory modes, duplicate headers,
stale/future timestamps, replayed request IDs, method/path/body/status tamper,
unsigned or tampered responses, oversized messages, unknown fields, trailing
JSON, UID/sandbox/netns reuse, and foreign cleanup state. Agent restart rotates
the key during `ADD`; bounded retries may recover only while deny remains
installed. Positive-control capture followed by zero direct TCP, UDP, DNS
UDP/TCP, and fragmented-UDP packets is mandatory. Authentication failure is an
availability failure and never permits fallback.

### Exact release lifecycle conformance

Install plans bind the complete observed source: deployed Helm revision,
release manifest and six runtime images, six CRD specifications, default
gateway-class UID/generation, and observation-certificate UIDs and public
digests. Negative tests require no mutation after changed source state, target
chart/CRD drift, wrong confirmation, partial CRD inventory, ambiguous Helm
state, mutable images, or certificate tampering. The initial beta transition
row permits only an identical served/storage CRD contract and rejects a schema
or storage change until an explicit storage-migration procedure exists.

Kind acceptance uses two immutable chart and release identities. It performs a
clean baseline install, source-bound forward transition, and separately planned
rollback. Both transitions require a newer Helm revision, exact target runtime
and CNI receipt, a new UID for the immutable target gateway class, preserved
certificate identity, live node capability, and the full gateway-replacement
startup-denial and packet exercise.
The Waycloak runtime transition must leave each existing singleton gateway Pod UID
unchanged even when its StatefulSet template advances to target gateway image
digests. The lifecycle fixture uses distinct source and target image digests.
Tests require an explicit `OnDelete` strategy, correction of a default
`RollingUpdate` strategy before template mutation, and no gateway Pod deletion until the
separate operator-confirmed activation step. That activation then requires a
new exact gateway Pod UID running the target images, protected denial during
loss, a current-generation Ready recovery binding, complete route/binding
cleanup, and zero direct packets. The lifecycle row asserts the Pod UID and
images after each disruptive verifier run; the privileged CNI row independently
counts packets while the application sandbox remains denied.
The staged template must also carry the exact target release version and
manifest digest as controller-owned runtime annotations while the live Pod keeps
its source annotations. This makes forward and rollback identity observable
even when an unchanged gateway binary is reused by both signed manifests.
Real-provider certification repeats that full sequence in the forward,
rollback, and final-forward directions. It samples application availability,
protected egress, ordinary-egress identity, gateway and workload Pod UIDs,
container restarts, target revision, and current-generation conditions across
both the signed transaction and explicit activation. A denied protected probe
is acceptable only as fail-closed availability loss; a direct-egress match is
an immediate release failure. Recovery duration and every gateway/node-agent
health transition remain evidence even when the final state is Ready. Core.20's
first amd64 K3s/Flannel row passed the no-leak and exact-identity requirements,
but the slower Core.19 rollback recovery remains open under #116 and cannot be
discarded merely because the final forward activation recovered quickly.

Before each supported forward and rollback transaction, the suite attempts the
same changed release through raw Helm. Connected rendering must refuse it with
the original class UID, deployed Helm revision, controller/CNI/node images, and
Ready route unchanged. Deterministic chart rendering separately requires the
early Argo class sync wave, proving that an offline direct sync reaches the
immutable boundary before executable runtime waves.
The staged-interruption row uses a real Helm command and kills the CLI process
after that command succeeds but before activation. It requires an immutable,
non-sensitive exact-plan journal; plan recovery with the identical plan ID;
degraded rather than stale healthy doctor output; no Pod IP or application
container for a newly enrolled Pod; no repeated staging mutation; exact target
activation; and journal cleanup. Unit/race negatives reject a missing, foreign,
tampered, differently targeted, or ambiguous checkpoint and prove exact
class-withdrawn, staged, and completed retries. The hosted pending/corrupt row
restores the exact pre-staging source record, corrupts the sole staged revision
into `pending-upgrade`, and requires an opaque-data digest without payload
disclosure, wrong-confirmation non-mutation, fail-closed startup, immutable
repair journaling before UID-preconditioned deletion, ordinary-install
exclusion, recovery after a post-deletion CLI kill, exact target activation,
and both-journal cleanup. Unit/race coverage repeats that contract for
class-withdrawn, staged, target, drift, and post-Helm/pre-cleanup interruption
checkpoints. Controller/node restart inside a Helm command and each distribution
snapshot row remain independent required evidence.

### Backup, restore, and disaster-recovery conformance

Portable backup tests require deterministic canonical identity and exact source
cluster, CRD, and gateway-class fingerprints. Canary credentials in Secrets,
ConfigMaps, metadata, status, bindings, allocations, provider mappings, and
runtime observations must be absent. Unknown fields, changed specs, reordered
inventories, missing CRDs/classes/namespaces, target drift, wrong confirmation,
and unowned name conflicts are negative gates.

Restore uses an explicit Kubernetes field manager and atomic create-only
semantics; it must refuse all conflicts before its first mutation and must not
adopt an object created during the final race window. Exact partial retry is
idempotent. Kind acceptance
deletes backed-up gateway/route intent, creates an enrolled Pod during the
missing-route window, and requires a Waycloak `FailedCreatePodSandBox` event
with no application container. Exact-plan restore then requires new gateway and
binding UIDs, current live readiness, protected probe success, and no imported
status or controller-owned binding. Distribution datastore-snapshot rows add
coherent UID/state recovery, restart, stale-observation withdrawal, and zero
direct-packet capture; a logical export never substitutes for that proof.

The hosted K3s row pins `v1.36.1+k3s1` and verifies the downloaded binary before
starting one embedded-etcd server with the bundled containerd and Flannel. The
same chained-CNI test takes a real distribution snapshot after an ordinary
five-protocol positive control and a denied enrolled Pod exist. It records exact
Namespace, Pod, and binding UIDs, creates post-snapshot drift, performs the
documented `--cluster-reset-restore-path` procedure with the retained root-only
token, and requires coherent UID rollback plus absence of the drift marker. The
restore gate first enumerates and removes every exact K3s containerd sandbox and
requires empty CRI container/sandbox inventories. It verifies the
snapshot-bound CNI binary, active chain, and durable attachment digest, restores
an independent Waycloak-owned first conflist before normal kubelet startup, and
rejects warm service-only recovery. It then runs the binding status reconciler
at a stale observation time, restarts the authenticated fixture agent under the
durable host deny state, repeats `CHECK`, no-start, and zero-packet assertions,
and finishes with idempotent `DEL`/`GC` and a second ordinary positive control.
The Kubernetes Pod UID remains exact while a recreated CRI sandbox identity may
change. Snapshot/restore commands are accepted only as a pair; identity drift,
missing objects, nonempty CRI, mismatched CNI recovery state, a non-first
Waycloak conflist, or a surviving marker are negative gates.

The production agent suite additionally proves that the CNI cannot supply a
data-plane configuration, stale binding UID/generation is rejected before
programming, partial configure or verify restores lockdown, drift repair occurs
under lockdown, and restart rebuilds only from validated durable attachments.
Restart recovery must first aggregate every durable attachment by exact Pod UID
and publish one group outcome. Exactly one live Ready sandbox may be verified;
its positive observation cannot be overwritten by missing or reused failed-ADD
attempts. A young LockedDown record retains the complete bounded-ADD grace
period. Old attempts are deleted only after the one exact Ready sandbox is
verified or exact Pod absence is authenticated. Multiple live sandboxes are
ambiguous: every exact namespace is locked down, one not-ready observation is
published, and all durable records remain quarantined. File-backed order,
agent restart, repeated reconciliation, DEL, and GC must converge without
status flapping or repeated cleanup.
Pod-bound TokenReview tests reject unbound tokens and cross-node observations.
Loss of the authenticated controller observation relay makes local status
unready, rejects new prepare, and locks down every durable attachment before a
recovered path can be re-verified. Only an absent exact Pod permits stale-netns
cleanup; API/watch ambiguity, deletion, node mismatch, and binding revocation
retain deny.

### Failure injection

Capture packets at the protected Pod, node/CNI interface, gateway overlay, and tunnel where possible. Inject:

- gateway Pod deletion;
- gateway node drain;
- tunnel interface removal;
- DNS failure;
- sustained engine-health failure, proving fast readiness withdrawal followed
  by an engine-container-only restart and automatic protected-path recovery;
- CRI Pod-sandbox recreation with a gateway endpoint projection change between
  prepare and verify, proving ordinary egress remains closed and the same Pod
  UID/IP recovers without waiting for the conventional repair container;
- stale desired generation;
- provider API timeout;
- controller/webhook restart;
- CNI packet loss and MTU mismatch;
- node-agent socket loss, wrong key, key rotation, replay and watch/RBAC loss.

The key assertion is absence of direct packets, not only expected application
errors. Recovery tests also assert stable gateway and workload Pod UIDs, stable
overlay and lease identities, and an increased VPN-engine container restart
count before readiness returns.

## Turnkey CLI and installation

Treat every `waycloakctl` output schema and mutation boundary as an API. Unit
tests require strict JSON decoding, exact plan recomputation and confirmation,
unsupported-cluster refusal before mutation, in-memory observation-key creation,
no private key in Helm values, exact UID-scoped disruption, and deterministic
support-bundle redaction with credential and endpoint canaries. CI builds every
CLI target twice, compares it byte-for-byte, generates an SPDX SBOM, signs exact
checksums using GitHub OIDC, and records build provenance.

Plan/apply tests also bind hashed cluster identity, exact node/runtime/kernel and
primary-CNI observations, overlay, and selected architecture. Any observation
drift must fail before Namespace, Secret, Helm, or CNI mutation. A mixed-
architecture fixture must refuse an implicit selection and prove that both
privileged DaemonSets target only the explicitly reviewed row.

A clean supported Kind or k3d row must exercise preflight, plan, apply, CNI
receipt verification, controller/node capability readiness, and exact rollback.
The lifecycle row must use two immutable releases in both directions and prove
that each changed-release staging revision retains the exact source node agent
until the target CNI receipt exists, then activates the target agent without a
missing-socket restart deadlock or any observed direct-egress packet.
It must terminate the CLI after a successful staging revision in both forward
and rollback directions, observe fail-closed startup and degraded health, and
resume the exact journal-bound plan without repeating the completed revision.
The row must include at least one durable Ready attachment across agent
replacement and prove lockdown, authenticated relay handshake, internal drift
reconciliation, capability republication, and continued rejection of public
CNI operations before readiness.
The same hosted row must rotate the observation relay identity with an exact
confirmation-bound plan. It injects failures after old-and-new overlap and
after the serving-key switch, requires the immutable journal and staged UID to
recover the identical plan, and proves CNI-ready scheduling remains withdrawn
while held agents publish fresh authenticated non-ready observations. Enrolled
Pods at both interruptions must receive neither an application container nor a
Pod IP. Completion must preserve stable Secret UIDs, remove old trust and all
staged private state, carry the final rotation identity into a later release
plan, restore fresh capability, and repeat the protected packet-loss exercise.
The disruptive verifier must delete Pods before route intent, observe each exact
Pod and UID-derived binding as absent, then observe route deletion before it may
emit `cleanupComplete=true`. A terminating binding is a failed cleanup result,
not a healthy resource to ignore during lifecycle certification.
The row also deletes a Pod whose chained `ADD` failed before any Ready
attachment existed. It must distinguish authenticated exact-Pod absence from
API/agent ambiguity, publish a zero-applied withdrawal from durable attachment
identity, release the binding finalizer within the cleanup bound, avoid foreign
netns cleanup, and retain state when the withdrawal report cannot be accepted.
It must also cover kubelet's early `DEL`: a failed-`ADD` sandbox may disappear
while the Pod remains pending, but its durable enrollment must survive until a
later exact-Pod-absence reconciliation publishes withdrawal. Accepted one-shot
withdrawals must not poison later node reports after the binding is deleted.
The relay must likewise accept a final-deletion race only as an idempotent no-op
while continuing to reject every cross-node or stale-identity mismatch.
Gateway replacement must advance binding intent while an older exact node
observation is queued: the relay ignores that old generation without mutating
status, the agent completes its handshake, and drift reconciliation adopts the
current generation without an authorization loop.
The clean-cluster exact-artifact gate measures preflight through a verified
protected workload and requires completion within 15 minutes. A separate
credentialed gate consumes the same signed release identity on a declared
Proton/OpenVPN support row, deletes the exact gateway Pod, and proves ordinary
egress continues while newly enrolled application containers cannot start
until the protected path recovers. Both gates are mandatory; combining them on
one newly provisioned node is not. Alpha presence is a preflight refusal, never
an automatic migration.

## Credentialed tests

Provider tests run only in protected CI environments or operator-owned clusters with short-lived credentials. Pull requests from forks never receive credentials. Logs and artifacts are redacted, retained minimally, and must not publish residential/provider-linked public IP history.

## Destructive alpha purge drills

Unit and fake-client tests require canonical target ordering, cluster/CA/UID
fingerprints, strict plan decoding, exact confirmation, UID-preconditioned
deletion, idempotent subset retry, and refusal on new/reused targets, finalizers,
or protected Pods. Credential, endpoint, spec, and status canaries must never
appear in plans or reports.

The destructive drill additionally proves the independent admission fence,
workload-owner suspension, runtime process/sandbox absence, alpha cleanup before
uninstall, exact CR/CRD deletion, fresh replacement install, new UID allocation
and provider mapping, and protected/unprotected verification. Packet capture
must record zero direct TCP, UDP, DNS UDP/TCP, and fragmented UDP before, during,
and after purge. A failed drill keeps protected workloads stopped and cannot be
waived into an ordinary-egress fallback.

The stable soak is a minimum 72-hour unchanged-artifact observation on the
operator's existing local cluster. qBittorrent is the sole application canary;
Bitmagnet remains scaled to zero, and no additional node is provisioned. Prior
cross-release canary history counts toward beta/lifecycle evidence but cannot
replace this exact-artifact epoch. The sustained Proton/qBitTorrent procedure is defined in
[real-provider port-forward acceptance](real-provider-port-forward.md). It is
an explicit, gated operator-cluster suite and is not replaced by the
protocol-faithful local fixture.

## Performance tests

Measure gateway CPU/memory, per-agent RSS, throughput, UDP packet loss, DNS latency, reconciliation duration, and disruption during membership changes at 1, 10, and 50 clients. Publish results with node/kernel/CNI/MTU context.

## Operational visibility tests

Collector unit tests enumerate the bounded label contract, current versus
stale conditions, missing conditions, unknown-reason collapse, enrolled-Pod
protection states, durable allocation states, partial Kubernetes list failure,
and privacy canaries. Rule and dashboard tests parse the published assets,
require explicit protection/availability/observability domains, and reject
object-derived dimensions or interpolated unreviewed labels. Helm renders the
optional assets twice and requires byte-identical output.

The exact turnkey Kind row scrapes the installed controller Service. It must
observe the installed class, a live Ready gateway with current positive tunnel
and DNS conditions, and a scheduled enrolled Pod whose missing route keeps it
in `binding_absent` without starting an application container. The same scrape
must not contain that Pod's namespace, name, UID, route, observer address, or
overlay. Generic controller reconciliation errors and explicit collection
health must be present. Conditions and Events remain the behavioral oracle;
the scrape is tested only as their bounded operational projection.

## Release gate

A release cannot rely on manual observation alone. Current release manifests
carry a canonical support matrix whose rows bind the exact platform, VPN
engine/provider configuration, feature identities, and named evidence suites.
The release inventory gate rejects a missing or altered certified row. Required
suites, artifact verification, supported-platform results, and any accepted
failures are attached to that release identity.

Post-publication Waycloak verification runs through
`hack/verify-release.sh` on a separate runner. It redownloads the exact
release inventory and uses pinned registry-native tooling rather than a Docker
daemon to verify blob and OCI signatures, SPDX attestations, GitHub provenance,
manifest-to-registry identity, exact amd64/arm64 indexes, Gluetun labels and
binary checksums, and byte-identical chart contents. Every external operation
has two bounded attempts; successful attestation payloads are suppressed while
bounded failure diagnostics and the exact artifact name remain visible.
