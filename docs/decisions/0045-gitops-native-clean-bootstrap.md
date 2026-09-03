# ADR 0045: GitOps-native clean bootstrap

Status: Accepted

Date: 2026-09-03

Amends [ADR 0032](0032-turnkey-bootstrap-and-preflight.md).

## Context

Waycloak's stable turnkey path uses `waycloakctl` to validate a cluster,
create observation certificates, install the controller before the privileged
CNI installer, and bind lifecycle state to an exact release. That assistant is
useful for interactive operations, but requiring it for every clean install
makes a conventional GitOps cluster unnecessarily difficult to express.

Users already choose established Kubernetes delivery machinery such as Helm,
Flux, Argo CD, or a configuration language. Waycloak should fit those choices
without introducing a Waycloak-specific release API, bootstrap operator, or
controller that merely recreates their reconciliation semantics. At the same
time, a declarative install must preserve exact artifact identity, certificate
ownership, controller-first CNI mutation, and the fail-closed contract.

## Decision

The versioned Waycloak Helm chart is the single canonical installation unit.
Each qualifying release publishes deterministic, exact-release bootstrap
assets generated from the signed release manifest:

- Helm values for direct Helm and other chart consumers;
- a Flux `OCIRepository` and `HelmRelease`; and
- an Argo CD `Application`.

All three forms consume the same OCI chart and the same values contract. Image
digests, chart digest, release identity, and certified cluster-profile defaults
come from the release manifest. Users retain ownership of cluster-specific
choices, beginning with the non-overlapping private overlay CIDR, namespace,
and credential provisioning.

The chart can create observation certificates with a scoped, non-privileged
Helm pre-install Job that runs the exact controller image. The Job is
idempotent and uses release-ownership annotations. The CNI installer does not
mutate host state until an unprivileged init container has observed the
controller readiness endpoint. This preserves the controller-first ordering
without an external CLI process.

KCL remains an optional authoring and validation layer for ordinary Helm
values. It is not a required runtime dependency. Flux and Argo CD remain
optional consumers; Waycloak has no runtime dependency on either project.

This declarative path initially covers clean installation only. Exact upgrade,
rollback, certificate rotation, repair, and removal continue to use the
qualified lifecycle workflow until equivalent GitOps transitions pass their
own fail-closed acceptance tests. Argo CD's generated example disables pruning
because removing networking CRDs and node components is a lifecycle operation,
not a routine synchronization side effect.

## Consequences

- A user can add a release-provided manifest plus a small cluster overlay to a
  Git repository and let their existing reconciler install Waycloak.
- Direct Helm, Flux, Argo CD, and KCL do not diverge into separate product APIs
  or charts.
- Release publication and verification gain three reproducible bootstrap
  assets and a rendered clean-install acceptance surface.
- The certificate Job temporarily has namespace-scoped Secret creation rights;
  its narrow ServiceAccount, exact Secret reads, hook lifetime, and release
  ownership checks limit but do not eliminate that bootstrap trust.
- GitOps lifecycle transitions remain deliberately incomplete until tested;
  clean-install support must not be described as safe declarative upgrade or
  removal.

## Alternatives rejected

- Introduce a `WaycloakRelease` CRD and bootstrap operator: duplicates the
  reconciliation systems users already operate and creates a new lifecycle
  dependency before Waycloak itself is installed.
- Maintain separate charts for direct Helm, Flux, and Argo CD: creates drift in
  security-sensitive defaults and ordering.
- Require `waycloakctl` for all installation: prevents a repository from being
  the complete declarative source of a clean install.
- Generate certificates with Helm template functions or cluster `lookup`:
  output becomes dependent on render context and is unreliable for controllers
  that render outside the target cluster.
- Require cert-manager: adds a hard runtime dependency where a bounded
  bootstrap Job is sufficient.
- Treat KCL as a deployment controller: changes an optional authoring tool into
  product-specific operational machinery.

## Related decisions

- [ADR 0004](0004-helm-oci-distribution.md)
- [ADR 0010](0010-external-webhook-certificate-ownership.md)
- [ADR 0031](0031-crd-installation-conversion-and-storage-lifecycle.md)
- [ADR 0032](0032-turnkey-bootstrap-and-preflight.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0042](0042-exact-release-transition-and-rollback.md)
