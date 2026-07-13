# ADR 0006: Native nftables and netlink data-plane backend

Status: Accepted
Date: 2026-07-13

## Context

The workload agent must establish a protected route without ever exposing a
temporary ordinary-egress path. It must also repair drift without flushing CNI
or application-owned networking state. Shelling out to distribution-specific
`ip`, `iptables`, or `nft` binaries makes atomicity, error handling, image
contents, and ownership difficult to reason about. Supporting both nftables and
iptables in the first release would double the most security-sensitive state
machine before the fail-closed proof exists.

## Decision

The initial Linux data-plane backend uses native Go APIs: `google/nftables` for
netfilter transactions and `vishvananda/netlink` for links, addresses, routes,
and VXLAN forwarding entries. The agent installs an owned nftables output-drop
base chain before it validates or creates the protected route. Subsequent
changes replace only the table derived from the current Pod UID and are applied
as atomic nftables transactions. Overlay and route operations carry similarly
deterministic names and are reconciled idempotently.

Linux behavior remains behind a small data-plane interface. Unsupported
kernels or missing features fail preflight and keep protected workloads
unready. Waycloak `v0.1` will not silently fall back to iptables commands or a
permissive mode. An iptables compatibility backend may be added later only with
the same packet-level fail-closed acceptance suite.

The gateway underlay endpoint is observed runtime state. It is delivered in the
UID-bound allocation ConfigMap; it is not inferred from desired registration or
resolved over an unrestricted network path by the workload.

## Consequences

- The first compatibility target requires Linux nftables, netlink, VXLAN, and
  `CAP_NET_ADMIN`.
- Agent images do not need shell networking tools at runtime.
- A backend can be replaced or supplemented without changing the Kubernetes
  selection API.
- Dedicated object names make ownership and drift repair inspectable.
- Clusters limited to iptables compatibility are rejected explicitly for the
  initial release.

## Alternatives rejected

- Shelling out to `nft` and `ip`: weaker error handling and a larger mutable
  runtime surface.
- iptables-first rules: harder atomic replacement and not the documented
  primary design.
- Simultaneous nftables and iptables support: premature duplication of the
  security-critical implementation and test matrix.
- Resolving a gateway name before lockdown: creates a startup escape window and
  makes the underlay endpoint ambiguous.
