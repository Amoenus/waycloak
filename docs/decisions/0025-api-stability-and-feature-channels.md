# ADR 0025: Clean-break API redesign and feature channels

Status: Accepted by issue #127
Date: 2026-07-26

## Context

Waycloak's current `networking.waycloak.io/v1alpha1` API was designed while
the data-plane proof was still evolving. It exposes implementation choices,
uses a Pod annotation as the workload API, and includes transitional fields.
There is one known user and no external compatibility promise. Preserving the
alpha shape would turn prototype decisions into permanent API debt.

Kubernetes API stability is valuable after the resource model is correct. It
is not a reason to retain a design that has not reached beta or stable status.

## Decision

Waycloak will make a deliberate clean break before its first stable API.

The current alpha resources and annotations are not compatibility constraints.
The replacement API is designed as one coherent set around
`VPNGatewayClass`, `VPNGateway`, `VPNEgressRoute`, `VPNWorkloadBinding`, and
the optional `PortForwardLease` and `WorkloadAdapter` contracts. The old
workload API is deleted. It is never translated, dual-written, imported, or
silently accepted by the replacement controller.

The cutover is an explicit destructive maintenance operation:

1. stop or scale down protected workloads while the existing fail-closed path
   remains installed;
2. remove the alpha controller and alpha custom resources using the documented
   teardown procedure;
3. install the replacement CRDs, controller, chained CNI plugin, and node agent;
4. author and apply new gateway, route, lease, and adapter manifests;
5. restart protected workloads and require the new conformance smoke test to
   pass before the cutover is complete.

There is no conversion webhook, object translator, dual-serving release,
deprecated alias, annotation bridge, or import of alpha runtime state. Old
manifests are reference material only and are not accepted by the new API.

Features are classified independently of release numbers:

- **Core** is installed by default, portable across the supported matrix, and
  must pass the complete fail-closed conformance profile.
- **Extended** is versioned and supported but optional; capability is reported
  before dependent intent is accepted.
- **Experimental** is disabled by default, isolated from stable kinds or fields
  where practical, and never silently falls back to Core behavior.

Stable `v1` graduation requires structural schemas, CEL validation, explicit
defaulting/reference/deletion/status contracts, upgrade and downgrade evidence,
and at least one release cycle in which the replacement beta API does not need
a breaking semantic change.

## Consequences

- Waycloak pays one destructive reinstall cost instead of carrying permanent
  dual-path behavior.
- Existing alpha manifests must be rewritten and the cutover requires a planned
  outage for protected workloads.
- The new API may stay alpha or beta longer; the `v1` label is reserved for a
  contract that can genuinely be maintained.
- After `v1`, ordinary Kubernetes compatibility rules apply and breaking
  changes require a new served version and migration plan.

## Alternatives rejected

- Convert every alpha field into the stable API: preserves accidental design.
- Run annotation and route attachment forever: creates two authorities and
  weakens security reasoning.
- Rename alpha to `v1` without field/lifecycle review: mistakes age for quality.
- Rewrite again after `v1`: spends the only compatibility-free window without
  using it.

## Related decisions

- [ADR 0026](0026-role-oriented-api-and-gateway-classes.md)
- [ADR 0027](0027-explicit-workload-opt-in-and-attachment.md)
- [ADR 0031](0031-crd-installation-conversion-and-storage-lifecycle.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
