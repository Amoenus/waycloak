# Stable and turnkey product implementation plan

Status: proposed clean-rewrite dependency graph
Last updated: 2026-07-26
Product requirements: [stable-turnkey-product.md](../product/stable-turnkey-product.md)

## Delivery rule

The replacement is allowed to break every alpha manifest. It is not allowed to
weaken fail-closed behavior while doing so. Work proceeds in roadmap order and
begins with the creation-time proof; class/route API implementation does not
outpace evidence that the target CNI lifecycle is enforceable.

The existing v0.4 eBPF research is reused as evidence, but neither the alpha
sidecar nor eBPF itself is presumed to be stable Core. CNI is the stable
lifecycle contract; its tested node backend may use nftables/netlink and later
eBPF without changing workload intent.

## Phase 0 — decision and feasibility gates

1. Accept or amend ADRs 0025–0034 as one decision set.
2. Freeze the replacement kind/ownership graph and API group/version.
3. Prototype chained CNI `ADD/DEL/CHECK/GC`, exact Pod UID resolution, bounded
   binding wait, deny-first installation, rollback and runtime restart.
4. Prove no application process can emit ordinary egress before successful
   Waycloak `ADD` on every proposed support-matrix row.
5. Update the threat model for node agent/CNI privilege and local protocol.

Exit: a reproducible Kind/k3d packet test demonstrates creation-time denial and
safe failure. If this fails, redesign before generating stable APIs.

## Phase 1 — replacement API and ownership

1. Inventory every alpha field/annotation/finalizer/status reason solely to
   prove complete removal and identify invariants worth redesigning.
2. Implement `VPNGatewayClass`, redesigned `VPNGateway`, `VPNEgressRoute`, and
   controller-only `VPNWorkloadBinding` schemas with CEL and list semantics.
3. Implement RBAC and admission that prevent user-authored bindings and reject
   alpha workload annotations.
4. Implement parent references, gateway-side route consent, denial privacy,
   watches and revocation.
5. Implement common conditions and per-parent route status.
6. Publish generated API reference and sample manifests from one source.

Exit: envtest proves ownership, authorization, generation freshness, deletion
and conflict semantics without any sidecar compatibility path.

## Phase 2 — node-owned stable data plane

1. Build the least-privilege local node-agent protocol and capability object.
2. Move durable allocation from ConfigMap projection to UID-scoped binding and
   agent cache.
3. Program deny, overlay, routing and DNS before CNI success.
4. Add drift repair, tunnel/gateway/member withdrawal and node restart recovery.
5. Remove injected init/sidecar components and mutation logic from replacement
   charts and code.
6. Run TCP, UDP, DNS UDP/TCP, fragmentation, tunnel loss, agent loss,
   controller loss, route deletion and gateway replacement E2E suites.

Exit: application containers have no Waycloak injection/capability and all
required failure modes record zero direct-egress packets.

## Phase 3 — gateway classes and Extended features

1. Move release-owned engine/image identity into the bundled gateway class.
2. Keep provider-native inputs and credential references namespaced on gateway.
3. Publish class/gateway/node feature identifiers and conformance profiles.
4. Redesign port forwarding around typed Service backend identity and prove
   single-active endpoint selection, drain, handoff and return path.
5. Revalidate `WorkloadAdapter` trust, digest immutability and renewable delivery.

Exit: unsupported features reject before programming; Core remains independent
of port forwarding and adapters.

## Phase 4 — clean cutover and turnkey workflow

1. Implement signed stateless `waycloakctl preflight`, `plan`, `install`,
   `doctor`, `smoke-test` and `support-bundle` commands.
2. Detect Kubernetes/CNI/runtime/kernel/architecture, cluster CIDRs, DNS,
   privilege/Pod Security, overlay conflicts and CNI chain support.
3. Generate reviewable Helm values, default-class gateway recipe, Secret refs,
   route/workload patches, rollback and purge instructions.
4. Drill workload stop, old deny preservation, alpha purge, replacement install,
   newly authored intent, restart and verification.
5. Make fresh install and cutover reach a verified workload within 15 minutes.

Exit: no source-code, image-digest, VNI, webhook-certificate or Gluetun-control
knowledge is needed on the supported quick path.

## Phase 5 — API beta, operations and stable certification

1. Publish signed standalone/chart CRD bundles and one support-matrix manifest.
2. Test every supported beta upgrade/rollback, stored-version migration,
   backup/restore, interrupted operation and safe uninstall/purge.
3. Complete disaster recovery (#32) and metrics/alerts (#33).
4. Run exact-artifact multi-day real-provider soak on every support-matrix row.
5. Publish SBOM, provenance, signatures, vulnerability results and conformance
   evidence.
6. Hold one release cycle without a breaking beta semantic change, then review
   `v1` graduation.

Exit: stable is an evidence-backed contract, not a version rename.

## Issue graph

New issue numbers are recorded here after creation. Dependencies use issue
links rather than relying on list order.

| Work item | Depends on |
| --- | --- |
| [#123 Stable rewrite epic](https://github.com/Amoenus/waycloak/issues/123) | — |
| [#124 CNI lifecycle feasibility spike](https://github.com/Amoenus/waycloak/issues/124) | #123 |
| [#125 Node-agent security/threat model](https://github.com/Amoenus/waycloak/issues/125) | #124 |
| [#126 Alpha API/runtime removal inventory](https://github.com/Amoenus/waycloak/issues/126) | #123 |
| [#127 Replacement API ownership freeze](https://github.com/Amoenus/waycloak/issues/127) | #124; #126 |
| [#128 Replacement schemas/generated APIs/RBAC](https://github.com/Amoenus/waycloak/issues/128) | #124; #126; #127 |
| [#129 Typed route and Pod enrollment](https://github.com/Amoenus/waycloak/issues/129) | #127; #128 |
| [#130 Cross-namespace parent consent](https://github.com/Amoenus/waycloak/issues/130) | #127; #129 |
| [#131 Common/per-parent status](https://github.com/Amoenus/waycloak/issues/131) | #127; #128 |
| [#132 UID binding/allocation protocol](https://github.com/Amoenus/waycloak/issues/132) | #124; #127; #128 |
| [#133 Node data-plane implementation](https://github.com/Amoenus/waycloak/issues/133) | #124; #125; #132 |
| [#134 GatewayClass/capability API](https://github.com/Amoenus/waycloak/issues/134) | #127; #128; #131 |
| [#135 Remove alpha sidecar/mutation/ConfigMap paths](https://github.com/Amoenus/waycloak/issues/135) | #126; #129; #132; #133 |
| [#136 Minimal admission and CNI-capable scheduling](https://github.com/Amoenus/waycloak/issues/136) | #124; #125; #128; #133 |
| [#137 Service-backed port-forward design](https://github.com/Amoenus/waycloak/issues/137) | #129; #132; #133 |
| [#138 `waycloakctl` preflight/doctor/smoke](https://github.com/Amoenus/waycloak/issues/138) | #128; #133; #134 |
| [#139 Destructive reinstall runbook](https://github.com/Amoenus/waycloak/issues/139) | #126; #128; #135 |
| [#140 CRD/release lifecycle and support matrix](https://github.com/Amoenus/waycloak/issues/140) | #128; #138; #139 |
| [#141 Stable certification](https://github.com/Amoenus/waycloak/issues/141) | replacement graph; #32; #33; #116 |

## Existing backlog treatment

- #6 and #107–#114 contain useful CNI/eBPF research; their preview assumptions
  must be reconciled with ADR 0034 rather than implemented blindly.
- #5 remains an Extended adapter integration.
- #31 remains post-Core multi-gateway/sharding.
- #32 and #33 become stable release dependencies.
- #64 supports the documentation-heavy program.
- #116 provides live gateway evidence but does not substitute for CNI Core soak.

## Acceptance matrix

| Area | Required evidence |
| --- | --- |
| CNI lifecycle | ADD/DEL/CHECK/GC, chain rollback, exact UID, runtime restart |
| Packet invariant | TCP/UDP/DNS/fragmentation under every startup/runtime failure |
| API | structural schema, CEL, unknown field rejection, default/list semantics |
| Ownership | RBAC, SSA managers, owner scope, finalizer bounds, uninstall behavior |
| References | local/cross namespace, consent revocation, privacy, requeue |
| Status | generation freshness, no-op writes, conflicts, stable reasons, parent keys |
| Identity | simultaneous allocation, restart, UID/name reuse, quarantine, restore |
| Security | node protocol, credentials, capabilities, support-bundle redaction |
| Turnkey | clean cluster and alpha cutover to protected workload |
| Release | signatures, SBOM, provenance, reproducibility, vulnerability policy |
| Operations | upgrade/rollback, DR, alerts, gateway replacement, multi-day soak |

## Cutoff rules

- No typed API implementation before creation-time feasibility evidence.
- No annotation or sidecar compatibility path in the replacement stable runtime.
- No CRD without a distinct owner, lifecycle, authorization and status need.
- No implicit enrollment, silent capability downgrade or alternate data plane.
- No stable claim for an untested Kubernetes/CNI/runtime matrix row.
- No Service-backed port forwarding until endpoint handoff and return symmetry
  are proven.
- Defer features rather than weaken identity, readiness or fail-closed behavior.
