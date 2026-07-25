# ADR 0029: Common status and condition contract

Status: Proposed
Date: 2026-07-26

## Context

Waycloak already reports granular observed conditions and treats `Ready` as
data-plane health rather than successful resource creation. As the API gains
classes, capability profiles, multiple backends, and stable versions, every
kind needs a consistent answer to four questions: was intent accepted, were
references authorized and resolved, was configuration applied, and is the
protected path actually healthy?

Gateway API has established widely understood `Accepted`, `ResolvedRefs`, and
`Programmed` conditions. Waycloak's stronger fail-closed readiness semantics
must be preserved rather than replaced by a weaker “configuration sent” bit.

## Decision

Every reconciled, user-facing Waycloak resource uses Kubernetes
`metav1.Condition` and the following positive-polarity summary conditions when
applicable:

- `Accepted`: the spec is syntactically and semantically acceptable and can
  produce some controlled configuration.
- `ResolvedRefs`: all required references exist, are compatible, and authorize
  the relationship.
- `Programmed`: the responsible Waycloak component observed the desired
  generation applied to the controlled data plane or runtime.
- `Ready`: the complete resource-specific runtime contract is observed healthy
  now.

`Ready` remains stricter than `Programmed`. For a `VPNGateway`, it includes the
observed tunnel, overlay, membership, DNS, forwarding, and enabled capability
paths. For `PortForwardLease`, it includes provider mapping, exact gateway
rules, target identity, renewable delivery, and required application
acknowledgement.

Component conditions such as `TunnelReady`, `MembershipApplied`, `DNSReady`,
`GatewayRulesReady`, and `Delivered` remain available for diagnosis. Summary
conditions are always present after first reconciliation. Every condition
includes the resource's current `metadata.generation` in
`observedGeneration`, a stable CamelCase reason, a concise non-sensitive
message, and a transition time that changes only when status changes.

`Unknown` means observation is unavailable; it is not silently converted to
`False` or `True`. Status writers use optimistic concurrency or explicit
server-side field ownership and suppress semantic no-op writes. If multiple
controllers can write a future status collection, entries are keyed by an
immutable controller name rather than competing for one field.

## Consequences

- Generic tooling and operators can reason about Waycloak resources using
  familiar Kubernetes semantics.
- Existing detailed conditions retain Waycloak's packet-path observability.
- Controllers and tests must migrate condition names and reasons compatibly.
- `Programmed=True` never authorizes application traffic or startup by itself.
- Status conformance becomes a release gate across every supported backend.

## Alternatives rejected

- Keep only resource-specific conditions: precise but difficult for generic
  automation and users to summarize.
- Use only `Ready`: conflates invalid intent, unresolved references, projection
  delay, and runtime failure.
- Treat desired ConfigMap publication as `Programmed`: observes API state, not
  the component that owns the data plane.
- Refresh transition timestamps on every poll: makes stable status appear to
  flap and defeats alerting.

## Related decisions

- [ADR 0020](0020-observed-admission-generation.md)
- [ADR 0021](0021-observed-membership-generation.md)
- [ADR 0023](0023-manager-owned-port-forward-generation.md)
- [ADR 0030](0030-capability-advertisement-and-conformance-profiles.md)
