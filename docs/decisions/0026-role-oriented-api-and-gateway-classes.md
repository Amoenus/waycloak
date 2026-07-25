# ADR 0026: Role-oriented API and gateway implementation classes

Status: Proposed
Date: 2026-07-26

## Context

The alpha `VPNGateway` mixes release-owned engine images and adapter identity
with operator-owned tunnel configuration. The alpha workload annotation then
lets an application owner address that combined object directly. This obscures
who may change implementation, infrastructure, and application attachment.

Gateway API demonstrates a durable Kubernetes pattern: separate infrastructure
provider, cluster operator, and application owner resources, then connect them
with typed references and status. Waycloak needs the same role clarity without
claiming that a VPN egress tunnel implements upstream Gateway API.

## Decision

The replacement API has these ownership roles:

| Persona | Owns | Responsibility |
| --- | --- | --- |
| Waycloak distribution/infrastructure provider | `VPNGatewayClass` | controller name, engine adapter, immutable image identity, defaults, supported features and conformance identity |
| Cluster/network operator | `VPNGateway` | class, placement, tunnel input references, address domain, route authorization and enabled capabilities |
| Workload owner | `VPNEgressRoute` plus a Pod-template route label | bind workloads to an authorized gateway and request workload-owned behavior |
| Workload owner | `PortForwardLease` when needed | request renewable inbound mapping to a typed backend |
| Waycloak controller | `VPNWorkloadBinding` and generated dependents | Pod-UID allocation, desired/applied state and observed health |

`VPNGatewayClass` is cluster scoped and has an immutable controller name.
`VPNGateway` is namespaced and references exactly one class. Credentials and
provider-native input remain namespaced references on the gateway; class
objects never contain account secrets.

`VPNEgressRoute` is namespaced with the workload, follows Gateway API-style
`parentRefs`, and is the canonical application-owner attachment API. Gateway
authorization controls which route namespaces may attach. Route status has a
per-parent entry so multiple controllers cannot contend for one undifferentiated
status field.

`VPNWorkloadBinding` is controller authored and Pod-UID scoped. It is readable
for diagnosis but is never user-authored intent. One reconciler may own each
primary kind; one manager binary may host several reconcilers. The manager
Deployment never owns user CRs or CRD declarations.

This replaces rather than wraps the alpha object model. Stable resources do not
carry inline engine image/type fields merely to make alpha manifests continue
to work.

## Consequences

- Each actor receives a focused API and RBAC boundary.
- A turnkey chart can install one tested default class without copying release
  internals into every gateway.
- Additional engines advertise capability behind a common intent contract.
- The API gains two meaningful kinds (`VPNGatewayClass` and `VPNEgressRoute`)
  and renames the internal attachment record.
- Class and route reference/status conformance become release gates.

## Alternatives rejected

- Keep expanding one `VPNGateway`: permanently mixes provider and operator
  ownership.
- Reuse upstream `GatewayClass`/`Gateway`: their listener and Route semantics do
  not cover whole-Pod, fail-closed VPN egress.
- Put provider credentials in a class: violates namespace and secret ownership.
- Create one CRD for every controller loop: controller topology is not a valid
  API reason.

## Related decisions

- [ADR 0027](0027-explicit-workload-opt-in-and-attachment.md)
- [ADR 0028](0028-reference-authorization-and-cross-namespace-consent.md)
- [ADR 0029](0029-common-status-and-condition-contract.md)
- [ADR 0030](0030-capability-advertisement-and-conformance-profiles.md)
