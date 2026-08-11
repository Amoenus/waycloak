# ADR 0027: Typed egress routes with explicit Pod-template enrollment

Status: Accepted by issue #127
Date: 2026-07-26
Supersedes on acceptance: ADR 0002 workload attachment contract

## Context

The alpha public workload API is a custom annotation containing a gateway
reference. It made the first proof easy, but an annotation cannot provide a
schema, reference authorization, independent status, policy ownership, or a
clean place for future portable behavior. Growing it would reproduce the
annotation-extension problems that Gateway API replaced for ingress.

Waycloak must also preserve a stronger invariant than ordinary selector-based
policy: protection is explicit and visible in the workload Pod template. The baseline
`PodSpec` has no extension field into which Waycloak can add a typed reference.

## Decision

The canonical attachment is a namespaced `VPNEgressRoute` custom resource.
The Pod template contains one reserved label whose value is the same-namespace
route name:

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: private
  namespace: media
spec:
  parentRefs:
    - group: networking.waycloak.io
      kind: VPNGateway
      namespace: network
      name: proton
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: downloader
  namespace: media
spec:
  template:
    metadata:
      labels:
        networking.waycloak.io/egress-route: private
```

The label is an enrollment and lookup key, not the policy API. It is deliberately
limited to a DNS-label-compatible route name so controllers, admission policies,
and node caches can index it using standard Kubernetes label machinery. Gateway
selection, DNS behavior, cluster-traffic policy, capabilities, authorization,
and status live in typed resources.

Rules:

1. A labeled Pod must resolve exactly one same-namespace `VPNEgressRoute`.
2. Missing, rejected, conflicting, or not-yet-programmed attachment fails closed;
   it never becomes ordinary egress.
3. An unlabeled Pod is not implicitly enrolled by namespace, selector, Service,
   or default gateway.
4. The route's gateway `parentRef` may cross namespaces only with gateway-side
   consent.
5. The alpha `networking.waycloak.io/gateway` annotation is rejected by the new
   API; there is no permanent compatibility bridge.
6. Additional annotation or label switches are forbidden. New user intent needs
   a typed field or a separately justified resource.

`VPNEgressRoute.status.parents[]` uses the referenced parent plus controller name
as identity and reports `Accepted`, `ResolvedRefs`, `Programmed`, and `Ready`.
Per-Pod allocation and observation live in controller-created
`VPNWorkloadBinding` objects keyed by Pod UID.

## Why this is not annotation-era design

The resource, not metadata, is the canonical API. It owns typed desired state,
authorization, lifecycle and status. The label only supplies the explicit Pod
enrollment signal that Kubernetes uses elsewhere for selection and that
Waycloak's security contract requires to remain visible in source. Removing all
Pod metadata would require either implicit selector enrollment or mutating user
workload templates, both weaker ownership models.

## Consequences

- Application owners get a declarative, inspectable attachment object.
- One label retains a turnkey workload experience after a route is provisioned.
- Route and gateway can evolve without proliferating workload annotations.
- A workload manifest and its route must be applied consistently; GitOps tools
  are the preferred transaction boundary.
- The cutover breaks every alpha workload manifest by design.

## Alternatives rejected

- Keep the gateway annotation as canonical: no typed lifecycle or status.
- Selector-only policy: enrollment can become implicit and ambiguous.
- Mutate Deployment/StatefulSet templates from a controller: creates field
  ownership conflicts and surprises GitOps.
- User-authored per-Pod binding CRs: ties intent to ephemeral names/UIDs and
  exposes allocation internals.
- Reuse Gateway API `HTTPRoute` or `GRPCRoute`: their traffic model is unrelated
  to whole-Pod default egress.

## Related decisions

- [ADR 0026](0026-role-oriented-api-and-gateway-classes.md)
- [ADR 0028](0028-reference-authorization-and-cross-namespace-consent.md)
- [ADR 0029](0029-common-status-and-condition-contract.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
