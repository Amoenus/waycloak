# ADR 0042: Exact release transitions bind source state and preserve denial

Status: Accepted implementation boundary for issue #32
Date: 2026-08-09

## Context

An immutable target chart does not by itself make an upgrade safe. A reviewed
plan can become stale after another release change, Helm does not upgrade CRDs
from a chart's `crds/` directory, and replacing the observation trust identity
during an ordinary transition can split controller and node-agent authority.
The singleton gateway also uses an `OnDelete` rollout because automatically
destroying its tunnel would turn a chart update into an uncontrolled outage.

Rollback has the same risks as forward upgrade. A Helm revision number is not
enough evidence of the chart, CRD, runtime image, class, CNI receipt, or
certificate identity that it would restore.

## Decision

`waycloakctl install plan` observes either an absent installation or one exact
deployed release. A deployed observation canonically binds:

- the sole deployed Helm revision;
- the release version and manifest digest reported consistently by the
  controller, CNI installer, node agent, and default gateway class;
- every digest-resolved Waycloak runtime image, including the gateway engine,
  gateway agent, and pause image;
- the gateway-class UID and generation;
- the observation CA and serving Secret UIDs plus public-certificate digests;
  and
- the exact six replacement CRD spec identities.

Apply repeats preflight, downloads the CRDs from the target chart at its exact
OCI digest, and repeats the installed-release observation before any namespace,
Secret, Helm, or CNI mutation. Any difference from the reviewed source refuses
the transition.

Before withdrawing the immutable class, apply creates one immutable,
release-scoped lifecycle ConfigMap containing the reviewed non-sensitive plan.
The journal binds its annotation and embedded plan to the same exact plan ID;
it contains no Secret value, private key, credential, endpoint, allocation, or
runtime observation. A foreign, mutable, malformed, differently targeted, or
unaccompanied journal is not recovery authority. The journal is deleted with a
UID precondition only after exact target postconditions pass.

The initial beta lifecycle row permits only an identical `v1beta1` CRD spec
inventory. Helm is not represented as a CRD upgrader. A changed schema, served
version, storage version, or conversion boundary requires a separate explicit
storage-migration plan following ADR 0031. This constraint supports a beta
release cycle with no API semantic change and makes rollback unable to
silently downgrade the stored contract.

Observation Secrets are owned by the stable Helm release, not by each plan.
Ordinary forward and rollback transitions preserve their exact UIDs, CA, and
serving certificate. First installation creates the serving Secret before the
public CA Secret, so an interrupted second write can reconstruct the CA from
the retained serving identity. A CA without its serving private key is not
silently regenerated; it requires the explicit certificate-rotation recovery
procedure.

`VPNGatewayClass.spec`, including its release identity, is immutable. A release
transition therefore first replaces only the node-agent executable with the
reviewed successor while retaining the source release identity and adding an
immutable-plan-bound capability hold. Startup and every reconciliation under
that hold install deny-first state for every durable attachment; ADD and CHECK
cannot reopen a path. Apply waits for the complete DaemonSet rollout and for
each current binding to contain a post-hold authenticated withdrawal
observation before it may delete the exact reviewed class UID with a Kubernetes
UID precondition. The held agent uses the reviewed plan digest as its bounded
instance identity, so the existing source relay can persist a cryptographic
acknowledgement without changing the frozen API or the relay JSON schema. It
then waits for that object to be absent and requires Helm
to create the same class name with a new UID and the exact target identity.
During this bounded class gap, gateways and enrolled workloads remain
unavailable behind the observed CNI deny path. The lifecycle must never weaken
class immutability or edit the old object in place.

The chart refuses a connected Helm render when the live default class has a
different exact release identity. That refusal occurs before Helm stores a new
revision or applies any executable component; only the journal-bound lifecycle
may first withdraw the reviewed UID. Argo CD renders without cluster-backed
`lookup`, so the class carries an early sync wave: a raw GitOps release change
fails on the immutable class before controller, CNI, or node-agent resources
advance. The supported GitOps handoff records the desired exact release, keeps
automatic runtime sync suspended, executes the reviewed `waycloakctl`
transition, then lets Argo verify/converge the already matching target. Argo is
not a release-transition authority or a runtime dependency.

Forward transition and rollback use the same confirmation-gated plan/apply
boundary with different independently verified target manifests. An existing
release never executes the controller-only clean-install bootstrap revision.
A changed release advances through one direct, journal-bound node-agent hold
and two fail-closed Helm revisions. The hold runs the successor node-agent image
with the exact source release identity, so it can authenticate to the source
controller and validate the source CNI receipt while refusing all local path
activation. The first Helm revision deploys the target controller, CNI
installer, immutable class, and successor node-agent image while preserving the
hold and source node-agent release identity. The target controller receives the
exact plan digest and exact source release identity for this staged revision.
Its authenticated observation relay accepts that source identity only when the
report is `Ready=False` and its instance identity equals the plan digest; a
positive report, foreign plan, or any other source identity remains rejected.
This keeps the withdrawal acknowledgement current after the controller changes
without making the relay dual-release capable outside the transaction. Apply
then replaces every stale
`OnDelete` gateway Pod and requires the exact target Pod and current Gateway
`Ready=True` observation while workloads remain denied. The second revision
removes the hold and activates the target node-agent release identity. This
breaks the installer/agent restart dependency without a CNI bypass, disabled
deny path, old-gateway traffic window, or ordinary-egress interval. Apply then
observes the exact target CRDs, runtime images, newly created immutable class
identity, and preserved certificate identity before reporting success.

The target agent starts by validating the target receipt and reinstalling deny
state for every durable attachment. It reports `Ready=False` through the
authenticated controller relay before attempting recovery. Only a successful
relay response authorizes its internal durable-state reconciliation; that path
bypasses the public CNI readiness gate so backend readiness does not depend on
itself. Public ADD/CHECK remain rejected until reconciliation succeeds and a
second report publishes the live target capability.

An interrupted CLI may resume only the original confirmed plan. Planning
returns that exact journaled plan when the requested verified manifest and
current preflight still match. Apply recognizes the following bounded state
families:

1. the exact reviewed source;
2. that source with the exact successor-agent hold installed and acknowledged;
3. the source or held source with only the exact class UID already absent;
4. a legacy class-replaced checkpoint, which must enter the same acknowledged
   hold before continuing;
5. one newer staged Helm revision with the target controller, CNI, pause,
   gateway images, and class while the successor agent remains held under the
   source release identity; or
6. the fully activated exact target.

Completed mutations are skipped. The staged state is verified before target
node-agent activation, and an already completed target is retry-idempotent.
Any other image/version mix, changed certificate or CRD, ambiguous deployed
Helm revision, missing journal, changed target, or changed cluster observation
is refused while the installed deny path remains authoritative. A Helm release
left in a pending/corrupt state is deliberately outside these checkpoints and
requires a separate explicit repair plan; recovery never guesses through it.

Gateway activation remains explicit within the confirmation-bound transaction.
After the staged controller and CNI report the target release, apply activates
each singleton gateway one at a time while the node-agent hold retains denial,
and verifies a fresh target gateway Pod plus current data-plane readiness before
releasing the hold. Qualification separately verifies route/binding recovery,
protected denial during loss, and no ordinary-egress fallback.
Each gateway Pod template carries controller-owned runtime annotations for the
exact release version and manifest digest. These annotations are rollout
evidence rather than user configuration or a compatibility API. They ensure
that a signed release transition stages an observable target revision even
when the target reuses an unchanged gateway binary digest.

### Observation certificate rotation

The replacement baseline has no admission webhook certificate. Its only owned TLS
boundary is the authenticated node-agent observation relay. Rotation therefore
uses a separate `waycloakctl certificate rotation plan/apply` transaction; an
ordinary release transition must preserve this identity and cannot overlap an
active certificate transaction.

The reviewed plan binds the cluster preflight, exact deployed Helm/runtime/
CRD/class identity, stable CA and serving Secret UIDs, public certificate
digests, and current node-agent rotation identity. Confirmation precedes
generation of a new private key. Apply stores that key only in one immutable,
release-owned staged Secret and records its UID and public digests in a separate
immutable, non-sensitive journal.

The controller validates the projected key pair on every new TLS handshake.
Apply publishes the old-and-new CA bundle before changing the serving key,
then rolls node agents with an explicit capability hold. Held agents keep the
local CNI and existing deny state operational but report `Ready=False`; the
controller records a fresh authenticated observation epoch without restoring
the CNI-ready scheduling label. After a held report succeeds through the new
serving certificate, apply prunes old trust, rolls agents against new-only
trust, requires another held observation, releases the hold, and finally
requires fresh live baseline capability.

Each single-object Secret update is an exact restart checkpoint, including
partial overlap publication and partial trust pruning. A retry accepts only the
original plan, journal, staged Secret UID/public digests, stable Secret UIDs,
unchanged release state, and one enumerated phase. Staged private material is
deleted before the journal; if cleanup is interrupted at that boundary, the
journal's target digests and exact live target authorize journal-only cleanup.
Missing private material at any earlier phase is a hard stop.

### Pending or corrupt Helm transition repair

An exact transition may leave one newer Helm storage Secret in
`pending-upgrade` or `failed` even though the live runtime remains at an
enumerated class-withdrawn, staged, or target checkpoint. Ordinary planning and
certificate rotation never guess through that state. The operator instead runs
`waycloakctl install repair plan`, reviews the exact source and stuck revision,
then supplies the repair plan ID to `install repair apply`.

The repair plan is available only while the original immutable transition
journal and its preflight still match. It binds the source deployed revision and
the sole newer stuck Secret by name, UID, Helm type, version, status, and a
digest of all labels, annotations, immutability and opaque data. The plan stores
only that digest, never the Helm release payload. Apply persists a second
immutable repair journal before deleting only the bound stuck Secret with a UID
precondition. It never edits Helm storage, relabels a revision, invokes opaque
Helm rollback, or selects among multiple candidates.

After deletion, apply resumes the original exact transition from its observed
checkpoint. A crash before Helm is retryable from the repair journal. A crash
after Helm succeeds is recognized only when one newer deployed revision and the
complete exact target runtime agree; cleanup then removes the transition and
repair journals without repeating Helm. Any candidate drift, extra revision,
preflight/chart/runtime regression, lost transition authority, or concurrent
install/certificate operation is refused while the installed deny path remains.

## Consequences

- A stale plan cannot cross an intervening release or certificate change.
- Rollback means applying a reviewed prior exact manifest, not trusting an
  opaque Helm history entry.
- The CNI chain and deny state stay installed throughout ordinary transitions;
  a successor agent uses the source identity under a plan-bound hold, and the
  target gateway reaches observed readiness before final activation.
- Every changed release receives a new immutable gateway-class UID at the same
  stable class name; attachment recovers only after the target class is live.
- Raw Helm and Argo release changes stop before creating a mixed-release
  runtime; GitOps convergence follows, rather than replaces, the reviewed
  transition transaction.
- CRD evolution remains unavailable until its storage migration is designed
  and tested explicitly.
- Exact class-withdrawn and post-staging CLI interruptions are resumable without
  repeating completed mutations. One journal-bound pending/corrupt Helm
  revision has a separate confirmation-bound repair transaction.
- The implementation reuses Kubernetes DaemonSet rollout/status semantics,
  server-side status observations, and maintained `client-go` polling. No new
  lifecycle or packet-filter library is introduced: the transaction ordering
  is Waycloak-specific, while packet denial remains the existing native
  nftables/netlink node-agent primitive. The staged relay allowance extends the
  existing authenticated publisher with an exact negative-report predicate;
  it does not add a second protocol, token format, or retry implementation.
- Observation certificate rotation is explicit and restart-safe. The declared
  single-server K3s datastore-snapshot row completes issue #32; additional
  distributions or datastore topologies are separate support expansion.

## Alternatives rejected

- Run `helm upgrade` or `helm rollback` directly: does not bind live source
  state, target CRDs, runtime identities, or postconditions.
- Rotate certificates on every release: creates mixed trust during rolling
  controller/node updates without adding security value.
- Let Helm silently leave old CRDs while claiming success: makes the runtime
  and API contract unverifiable.
- Automatically restart every singleton gateway outside the reviewed
  transition: creates an unbound tunnel outage and can invalidate active
  provider mappings without the retained node-agent deny hold.
- Force a changed CRD schema during rollback: can corrupt or hide stored beta
  fields and violates the explicit storage lifecycle.

## Related decisions

- [ADR 0031](0031-crd-installation-conversion-and-storage-lifecycle.md)
- [ADR 0032](0032-turnkey-bootstrap-and-preflight.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0037](0037-uid-bound-allocation-and-quarantine.md)
- [ADR 0041](0041-portable-state-backup-and-disaster-restore.md)
