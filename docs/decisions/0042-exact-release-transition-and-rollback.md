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
- every digest-resolved Core runtime image, including the gateway engine,
  gateway agent, and pause image;
- the gateway-class UID and generation;
- the observation CA and serving Secret UIDs plus public-certificate digests;
  and
- the exact six replacement CRD spec identities.

Apply repeats preflight, downloads the CRDs from the target chart at its exact
OCI digest, and repeats the installed-release observation before any namespace,
Secret, Helm, or CNI mutation. Any difference from the reviewed source refuses
the transition.

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

Forward transition and rollback use the same confirmation-gated plan/apply
boundary with different independently verified target manifests. An existing
release never executes the controller-only bootstrap revision. Helm must
advance to a new deployed revision, and apply then observes the exact target
CRDs, runtime images, class identity, and preserved certificate identity before
reporting success.

Gateway activation remains explicit. After the controller, CNI installer, and
node agent report the target release, the operator activates each singleton
gateway one at a time during a declared fail-closed window and verifies a fresh
gateway Pod UID, route/binding recovery, protected denial during loss, and no
ordinary-egress fallback.

## Consequences

- A stale plan cannot cross an intervening release or certificate change.
- Rollback means applying a reviewed prior exact manifest, not trusting an
  opaque Helm history entry.
- The CNI chain and deny state stay installed throughout ordinary transitions;
  existing releases never temporarily disable the node components.
- CRD evolution remains unavailable until its storage migration is designed
  and tested explicitly.
- Interrupted Helm transitions and distribution datastore snapshots remain
  additional issue #32 certification rows.

## Alternatives rejected

- Run `helm upgrade` or `helm rollback` directly: does not bind live source
  state, target CRDs, runtime identities, or postconditions.
- Rotate certificates on every release: creates mixed trust during rolling
  controller/node updates without adding security value.
- Let Helm silently leave old CRDs while claiming success: makes the runtime
  and API contract unverifiable.
- Automatically restart every singleton gateway: creates an unreviewed tunnel
  outage and can invalidate active provider mappings.
- Force a changed CRD schema during rollback: can corrupt or hide stored beta
  fields and violates the explicit storage lifecycle.

## Related decisions

- [ADR 0031](0031-crd-installation-conversion-and-storage-lifecycle.md)
- [ADR 0032](0032-turnkey-bootstrap-and-preflight.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0037](0037-uid-bound-allocation-and-quarantine.md)
- [ADR 0041](0041-portable-state-backup-and-disaster-restore.md)
