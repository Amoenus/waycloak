# ADR 0032: Turnkey bootstrap uses Helm plus an optional stateless assistant

Status: Accepted by issue #127
Date: 2026-07-26

## Context

The current first-workload path requires users to prepare webhook TLS, identify
Pod and Service CIDRs, choose a non-overlapping overlay CIDR and VNI, understand
Pod Security exceptions, create native engine inputs, apply authorization
labels, and interpret several readiness layers. Those are real security and
networking decisions, but requiring every user to discover them manually works
against a turnkey product.

Helm must remain the primary installer, plain Kubernetes the source of truth,
and certificate/private-key material must not enter Helm values or release
metadata. No external operator may become a mandatory runtime dependency.

## Decision

Waycloak keeps a fully documented Helm-only path and adds an optional,
stateless `waycloakctl` bootstrap and diagnostics assistant. The assistant is a
signed release artifact, not a controller dependency or alternate API.

The assistant supports read-only planning before mutation:

- `waycloakctl preflight` reports Kubernetes, CNI/runtime, node kernel
  features, Pod Security, admission reachability, cluster CIDRs, conflicting
  overlays/VNIs, and supported Waycloak profiles;
- `waycloakctl install plan` produces the exact Helm command, values, CNI
  changes, security-policy changes, and rollback steps;
- `waycloakctl gateway init` renders a minimal `VPNGateway`, native engine
  ConfigMap, and Secret references from a reviewed provider/engine recipe;
- `waycloakctl doctor` traces `Accepted`, `ResolvedRefs`, `Programmed`, and
  `Ready` without displaying credentials or sensitive endpoints;
- `waycloakctl verify` creates an isolated protected and unprotected smoke
  test, proves distinct/fail-closed behavior, and removes only its own objects.

Mutation requires `--apply` or explicit confirmation. Broad namespace Pod
Security changes, CRD upgrades, CNI modification, credential creation, and
destructive cleanup are never implicit.

For a clean install, one confirmation-bound apply uses two Helm revisions. It
first starts the controller with the CNI installer, node agent, and default
class disabled, then activates the exact reviewed Core values only after that
bootstrap revision is Ready. Existing deployed releases skip the bootstrap
revision. This ordering avoids making the chained CNI authoritative before its
control plane exists without exempting application namespaces or weakening
deny-first ADD behavior.

The review record is bound to one canonical preflight observation. That digest
includes hashed cluster identity plus exact Kubernetes/runtime/kernel,
architecture, CNI, and network facts. Apply re-runs preflight and refuses drift
before creating any object. Mixed-architecture clusters require the operator to
select one explicit `amd64` or `arm64` row; the CNI installer and node agent are
scheduled only there, so an unproved architecture cannot advertise Core
capability merely because its image was built.

If a minimal dynamic admission webhook remains necessary, its TLS follows ADR
0010. Static mutation and validation prefer stable declarative admission policy
on supported Kubernetes versions. Neither admission mechanism is the packet
security boundary.

Detected cluster CIDRs and overlay suggestions are presented for approval and
recorded in rendered intent. They are not silently trusted. Provider recipes
use engine-native configuration, immutable tested images/classes, and Secret
references; they do not duplicate provider credentials into generated files.

The target supported path is installation to one verified protected workload
within 15 minutes for a compatible cluster and supported provider, using one
typed route and one route-name label in the workload template.

## Consequences

- New users get guided safe defaults without hiding security decisions.
- Advanced and GitOps users retain ordinary manifests, Helm, and immutable OCI
  artifacts.
- The CLI requires its own signatures, SBOM, provenance, compatibility tests,
  and stable output schemas where automation consumes them.
- Some prerequisites cannot be fixed automatically; turnkey means precise
  diagnosis and safe generated intent, not bypassing cluster policy.
- Provider recipes become tested UX assets while engine-native configuration
  remains the product boundary.

## Alternatives rejected

- Put all discovery and random generation in Helm templates: nondeterministic,
  cannot safely inspect the cluster, and risks putting secrets in release data.
- Require cert-manager, a secret operator, or a policy engine: violates the
  plain-Kubernetes runtime boundary.
- Automatically label namespaces privileged: expands tenant authority without
  an explicit security decision.
- Hide incompatible kernels/CNIs and try anyway: turns setup simplicity into
  runtime ambiguity.

## Related decisions

- [ADR 0004](0004-helm-oci-distribution.md)
- [ADR 0010](0010-external-webhook-certificate-ownership.md)
- [ADR 0017](0017-engine-native-configuration-boundary.md)
- [ADR 0030](0030-capability-advertisement-and-conformance-profiles.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
