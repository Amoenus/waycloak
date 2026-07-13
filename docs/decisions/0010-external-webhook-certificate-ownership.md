# ADR 0010: External ownership of admission webhook certificates

Status: Accepted
Date: 2026-07-13

## Context

The admission webhook is on the fail-closed startup path for protected Pods.
Its serving certificate and the API server's CA bundle must rotate without an
interval where opted-in Pods are admitted without mutation or rejected because
the webhook presents an untrusted certificate. Generating a private key during
Helm rendering would make releases nondeterministic, put credential lifecycle
inside a package manager, and make rollback and rotation ownership ambiguous.
Requiring cert-manager would conflict with Waycloak's plain-Kubernetes runtime
boundary.

## Decision

The Helm chart requires an existing `kubernetes.io/tls` Secret in the release
namespace and the matching CA bundle as explicit values. The chart never
generates a private key, self-signed certificate, or random certificate
material. Certificate issuance and renewal remain the cluster operator's
responsibility and may be performed manually or by any external certificate
controller.

Waycloak does not have a runtime dependency on cert-manager. An optional
cert-manager integration may be published later as a separate adapter that
produces the same Secret and CA-bundle inputs; the primary chart and controller
remain unaware of it.

Rotation uses a two-phase trust transition: publish a CA bundle containing the
old and new CAs, replace the serving Secret and roll the controller Deployment,
verify fail-closed admission through the new certificate, then remove the old
CA. Rollback retains the same externally owned Secret contract.

## Consequences

- Installation requires certificate preparation before Helm can render valid
  production resources.
- Private keys never enter Helm values, release metadata, or generated chart
  artifacts.
- Operators can use their existing PKI without adding a Waycloak runtime
  dependency.
- Automated certificate controllers must coordinate CA-bundle rotation using
  the documented two-phase procedure.

## Alternatives rejected

- Generate certificates in Helm templates: nondeterministic and unsafe for
  upgrade, rollback, and key custody.
- Generate certificates in a post-install Job: adds broad Secret-writing RBAC
  and a second lifecycle controller on the admission path.
- Require cert-manager: violates the dependency boundary and excludes plain
  Kubernetes installations.
- Replace the CA and serving certificate atomically: API servers and webhook
  replicas may observe the two objects in different orders.
