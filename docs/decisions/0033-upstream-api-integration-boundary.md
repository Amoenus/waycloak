# ADR 0033: Upstream Kubernetes API integration without semantic mimicry

Status: Proposed
Date: 2026-07-26

## Context

Waycloak is a Kubernetes networking extension, but no stable upstream API
currently represents its complete contract: bind all external traffic from an
explicitly opted-in Pod to a selected stateful VPN tunnel and block that
traffic before startup and during every protected-path failure. Gateway API is
the future of ingress and mesh L4/L7 routing; CNI defines container network
lifecycle; Kubernetes CRDs, admission, conditions, owner references, CEL,
Services, Secrets, and NetworkPolicy cover other parts of Waycloak's domain.

Reusing a familiar kind name without matching its semantics creates worse
interoperability than an honest domain-specific API.

## Decision

Waycloak uses the narrowest upstream API whose semantics fully match each
boundary and keeps Waycloak CRDs for the remaining domain intent.

- Gateway API design principles guide role separation, references, status,
  capability discovery, conformance, and extension policy. Waycloak does not
  claim to implement Gateway API for private egress.
- `ReferenceGrant` is preferred for compatible cross-namespace references.
- CNI chaining is the Core creation-time enforcement boundary and preserves
  `prevResult`, `ADD`, `CHECK`, `DEL`, `GC`, ordering, and unrelated state.
- Kubernetes `Service` becomes the preferred future stable abstraction for
  rolling port-forward targets only after UID, endpoint handoff, drain, and
  cross-namespace semantics are proven.
- Secrets and ConfigMaps remain referenced standard inputs; credential values
  never move into CR specs or status.
- OpenAPI/CEL perform structural and object-local validation. Stable declarative
  admission policy handles static admission behavior where available. Webhooks
  are reserved for dynamic authorization/reference checks; neither admission
  path substitutes for CNI enforcement.
- Owner references are used only for real same-scope lifecycle dependency;
  finalizers are bounded and reserved for external cleanup or quarantine.
- Server-side apply is used only with documented field managers and ownership;
  it is not a blanket replacement for conflict-aware reconciliation.

An upstream watch is part of release planning. Gateway API egress work,
generalized reference authorization, CNI/runtime identity, Kubernetes network
policy evolution, and admission-policy capabilities are reviewed before each
stable API milestone. Adoption requires a migration and conformance plan.

## Consequences

- Waycloak remains Kubernetes-native without acquiring unnecessary hard
  dependencies.
- Users can distinguish standards compliance from standards-inspired design.
- Some Waycloak-specific CRDs remain necessary and must carry full API quality
  obligations.
- Optional adapters can integrate Gateway API or other ecosystems without
  entering the fail-closed Core profile.
- Upstream changes may supersede local APIs after `v1`, requiring an explicit
  future version decision rather than indefinite duplication.

## Alternatives rejected

- Model `VPNGateway` as upstream `Gateway` despite incompatible listener and
  egress semantics: misleading and non-conformant.
- Ignore upstream patterns because Waycloak is specialized: repeats solved API,
  authorization, and lifecycle mistakes.
- Add Gateway API as a mandatory runtime dependency for appearance: increases
  installation scope without implementing its routing contract.
- Replace the cluster's primary CNI wholesale: a chained plugin is the narrower
  lifecycle extension and preserves the primary network implementation.

## Related decisions

- [ADR 0027](0027-explicit-workload-opt-in-and-attachment.md)
- [ADR 0028](0028-reference-authorization-and-cross-namespace-consent.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
