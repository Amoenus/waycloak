# ADR 0019: Conformance-gated optional eBPF data plane

Status: Proposed
Date: 2026-07-15

## Context

ADR 0006 selects native nftables and netlink as the proven initial backend and
keeps Linux behavior behind a data-plane interface. An eBPF implementation may
reduce rule-management overhead or enable more efficient observation, but
kernel version alone does not establish usable BPF support. Kernel build
configuration, BTF, helpers, verifier behavior, architecture, lockdown mode,
CNI programs, attachment points, and privileges all affect viability.

The available homelab currently includes amd64 and Raspberry Pi arm64 nodes on
Linux 6.12 kernels. Those nodes are useful evaluation targets, not evidence of
support or incompatibility.

## Proposed decision

Evaluate eBPF only as an explicit optional implementation of the existing
data-plane contract. The Kubernetes workload and gateway selection APIs,
stable identities, allocation handshake, overlay semantics, conditions, and
fail-closed threat model remain backend-independent.

An eBPF backend may be accepted only if it passes the same packet-level
conformance suite as nftables/netlink for startup, tunnel loss, gateway loss,
agent restart, drift, detach, upgrade, and cleanup. It must attach without
replacing or flushing CNI-owned programs, maps, routes, or hooks.

Preflight reports required capabilities per node. Backend selection is
explicit and schedulable. A Pod selected for eBPF must not fall back to a
different or permissive backend at runtime. Unsupported nodes fail admission,
preflight, or scheduling with a stable reason before applications can start.

Issue #65 will determine viable hooks, feature requirements, privilege model,
architecture/CNI compatibility, and measured performance. Adoption requires a
follow-up accepted ADR containing that evidence. Until then, ADR 0006 remains
the only supported production backend decision.

## Consequences

- eBPF experimentation cannot weaken or bypass the existing guarantees.
- ARM64 and Raspberry Pi support is evidence-based rather than assumed from
  architecture or kernel version.
- The data-plane interface and conformance tests become explicit product
  boundaries.
- Mixed-capability clusters require clear scheduling and preflight behavior.
- Maintaining two backends is justified only by measured operational value.

## Alternatives rejected

- Replace nftables/netlink before conformance evidence exists: discards the
  proven backend and expands the security-critical surface prematurely.
- Infer support from kernel version: ignores build configuration, BTF,
  helpers, lockdown, CNI ownership, and verifier differences.
- Silently fall back after eBPF attachment failure: creates backend-dependent
  startup behavior and can open an egress window.
- Require eBPF cluster-wide: excludes otherwise supported lightweight nodes
  and CNIs without a demonstrated product need.
