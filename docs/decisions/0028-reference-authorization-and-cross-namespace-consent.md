# ADR 0028: Cross-namespace references require target-side consent

Status: Proposed
Date: 2026-07-26

## Context

Workloads and leases may reference a gateway in another namespace. Waycloak
currently authorizes that relationship through
`VPNGateway.spec.workloadAccess.namespaceSelector`, which resembles Gateway
API route attachment. Future classes, policies, Service targets, Secrets, and
implementation parameters could create additional cross-namespace references
and confused-deputy risks if each invents different authorization semantics.

Kubernetes owner references cannot cross namespace boundaries. Authorization,
lifecycle ownership, and discovery are separate concerns.

## Decision

Every cross-namespace reference requires explicit consent from the owner of
the referenced namespace or object.

Workload and `PortForwardLease` attachment to `VPNGateway` retains the
gateway-side allowed-namespace handshake. Selectors are evaluated against an
uncached or coherently observed Namespace object, documented as security
policy, and accompanied by guidance that access labels must not be writable by
the consuming tenant.

Other cross-namespace references use upstream `ReferenceGrant` when its scope
and availability meet Waycloak's supported Kubernetes matrix. If Waycloak must
temporarily define its own grant, it follows the same additive `from` and `to`
model, lives in the target namespace, excludes name-based pseudo-security, and
has an explicit migration path to the upstream type.

Unauthorized references produce `ResolvedRefs=False` with reason
`RefNotPermitted`. Status and events do not reveal whether a target object
exists until consent permits that discovery. Deleting or changing a grant or
allowed-namespace rule requeues every dependent resource and withdraws
programming/readiness fail closed.

Same-namespace references remain the default. Cross-namespace owner references
are never created; labels, indexes, explicit references, bounded finalizers,
and controller reconciliation implement those lifecycles.

## Consequences

- All future reference types share a predictable authorization philosophy.
- Gateway owners retain control over which workload namespaces consume a
  tunnel.
- Reference changes become watched runtime dependencies rather than admission-
  time checks only.
- Supporting upstream `ReferenceGrant` may add an optional or required Gateway
  API CRD dependency; that decision must be explicit for each release.
- Tenant-controlled namespace labels cannot safely authorize themselves.

## Alternatives rejected

- Permit any namespace to reference any gateway: enables cross-tenant resource
  use and port-forward confused-deputy attacks.
- Use RBAC alone: authorization to create a referring object does not imply
  consent from the target owner.
- Use cross-namespace owner references: invalid in Kubernetes and unsafe for
  garbage collection.
- Create a different grant kind for every Waycloak reference: increases API
  surface and inconsistent security behavior.

## Related decisions

- [ADR 0002](0002-kubernetes-api-and-admission.md)
- [ADR 0012](0012-port-forward-lease-identity-and-target-binding.md)
- [ADR 0029](0029-common-status-and-condition-contract.md)
