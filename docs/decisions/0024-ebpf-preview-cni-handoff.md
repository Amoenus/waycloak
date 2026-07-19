# ADR 0024: Prototype eBPF through a CNI creation-time handoff

Status: Accepted
Date: 2026-07-19

## Context

ADR 0019 requires evidence before eBPF becomes a supported optional backend.
The v0.4 research proved that a pinned Pod-parent `cgroup_skb` deny survives
loader exit and covers ordinary init, native sidecar, and application container
cgroups on the sampled k3s/containerd topology. Atomic link replacement and
explicit detach behaved fail closed on representative amd64 and arm64 nodes.

The same probes showed that `cgroup_skb` cannot implement Waycloak's VXLAN,
routing, DNS DNAT, or lease DNAT responsibilities on those nodes. A Pod-local
filter substitution therefore retains the current privileged components while
adding BPF privilege and has no demonstrated product value.

Node discovery after sandbox creation has an unavoidable first-packet race.
The CNI specification instead requires the runtime to create the network
namespace, run chained `ADD` operations in order, and halt on error. The exact
containerd 2.2.3 integration additionally passes the Pod UID and cgroup parent,
through go-cni's runtime-specific `cgroupPath` capability, alongside the netns.

## Decision

`v0.4.0` will pursue a developer-preview E2 prototype: an optional chained CNI
plugin installs and pins Pod-parent eBPF default-deny during sandbox creation,
and a prepared-node agent adopts and reconciles the exact UID/generation-owned
state.

The current Pod-local nftables/netlink backend remains the supported default.
Preview selection is explicit on the Pod template, is scheduled only to nodes
with administrator preparation intent and a current executable capability
observation, and never silently falls back.

The prototype does not become releasable merely by attaching eBPF. Before the
preview is exposed, the node architecture must own and pass the complete
declared feature subset, remove the privileged Waycloak networking sidecar or
demonstrate another accepted material benefit, pass the backend-neutral
fail-closed suite, and document the node-wide threat boundary. Unsupported
feature combinations are rejected rather than partially routed.

Initial compatibility is deliberately narrow: the proved k3s/containerd and
Flannel chain on amd64 and arm64 prepared nodes. `cgroupPath` is not a
well-known portable CNI capability, so each additional runtime requires a
separate adapter and conformance decision.

## Consequences

- The promising path can remove per-Pod networking privilege and components,
  but installation now touches runtime-owned CNI binaries/configuration and
  introduces a privileged node component.
- CNI install, ordering, idempotence, `CHECK`, `DEL`, `GC`, upgrade, rollback,
  node reboot, and uninstall become product lifecycle obligations.
- Required node affinity handles initial placement only; the node agent owns
  runtime health and fail-closed response after scheduling.
- Dynamic VXLAN, route, DNS NAT, and lease NAT ownership is the next threat-model
  and feasibility gate. Namespace entry requiring `CAP_SYS_ADMIN` is not
  accepted implicitly.
- Application-specific lease adapters remain separate from network enforcement.
- ADR 0006 remains normative for the supported production backend. This ADR
  refines ADR 0019's research outcome and does not supersede it.

## Alternatives

- **Pod-local eBPF filter:** rejected as a release mode because it retains the
  existing sidecar, init gates, and `NET_ADMIN` while adding BPF complexity.
- **Node discovery after sandbox creation:** rejected because it cannot prove
  deny-before-first-packet.
- **Host-veth tc/TCX complete data plane:** deferred because it immediately
  expands into tunnel, translation, checksum, conntrack, and CNI coexistence
  work without first testing the smaller creation-time handoff.
- **Make eBPF the default:** rejected because heterogeneous clusters and
  unprepared nodes remain valid supported environments.
- **Automatic fallback:** rejected because it changes the declared enforcement
  mechanism during failure and can create an untested egress window.
