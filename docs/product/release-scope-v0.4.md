# Waycloak v0.4.0 product requirements

Status: Draft for implementation review
Last updated: 2026-07-19
Evidence: [eBPF data-plane research](../research/ebpf-data-plane.md)
Decision: [ADR 0024](../decisions/0024-ebpf-preview-cni-handoff.md)

## Product decision

`v0.4.0` is the optional eBPF node-data-plane developer preview. It validates
the E2 architecture selected by the research: a chained CNI creation-time
handoff installs a Pod-cgroup eBPF deny boundary, and a node agent adopts and
reconciles the protected path.

The existing Pod-local nftables/netlink sidecar remains the supported default.
The preview is disabled unless both the cluster operator prepares selected nodes
and the workload author explicitly requests it. There is no automatic backend
selection and no fallback between modes.

A cluster may run both modes concurrently. Nodes that are not prepared for eBPF
remain fully supported sidecar targets; eBPF capability is never a cluster-wide
installation prerequisite.

The release is not complete merely because a BPF program loads. It must prove
that the optional node architecture preserves deny-before-route and can remove
the privileged Waycloak networking sidecar for a declared feature subset. If
node ownership of the complete selected path cannot meet the gates below, the
preview is not shipped.

## Target users and value

The preview is for cluster operators who deliberately prepare Linux nodes and
want to evaluate lower protected-Pod component overhead. It is not required for
users whose nodes, runtime, CNI, LSM policy, or operational model do not support
eBPF.

The primary value hypothesis is consolidation: replace per-workload privileged
networking components and their polling loops with one node owner while keeping
application containers capability-free. Performance is a measured secondary
hypothesis, not a release claim. Application-specific lease adapters remain
orthogonal.

## Compatibility boundary

- Default mode: the current supported Pod-local nftables/netlink sidecar.
- Preview mode: Linux cgroup v2, bpffs, BTF, required BPF helpers and link
  operations, and an approved LSM/seccomp/capability profile on an explicitly
  prepared node.
- Initial runtime/CNI target: k3s with containerd 2.2.3 and the observed CNI
  1.0.0 Flannel, portmap, and bandwidth chain.
- Initial architectures: amd64 and arm64, each independently conformance-tested.
- Other runtimes and CNIs are unsupported until their creation-time identity,
  cgroup handoff, ordering, attachment composition, and rollback are proved.
- Node Feature Discovery may publish supporting facts, but it is optional and
  never substitutes for Waycloak's executable preflight.

## Public selection and status contract

A protected workload continues to select its gateway with
`networking.waycloak.io/gateway`. It requests the preview with this additional
Pod-template annotation:

```yaml
networking.waycloak.io/data-plane: ebpf-preview
```

Omitting the annotation selects `sidecar`. Unknown values are rejected.
Admission does not silently rewrite an unavailable preview request to sidecar.

Prepared-node state has two independent inputs:

1. administrator intent, represented by a protected node-preparation label;
2. an executable capability observation produced by the Waycloak node agent.

Admission adds required initial node affinity for both. Because Kubernetes node
affinity is ignored after scheduling, the running node agent remains the live
authority. Loss of executable capability retains or installs deny, reports the
workload data plane not ready, and never changes the selected mode.

Detailed per-node observations must include architecture, kernel release,
runtime/CNI identity, cgroup mode, BTF/bpffs state, attach/update/pin results,
LSM profile result, capability-contract version, last probe time, and stable
failure reasons. `VPNWorkload` status must expose requested mode, observed mode,
node, attached generation, and observed data-plane readiness.

## Required components and ownership

### Prepared-node installer

- Installs immutable amd64/arm64 CNI and node-agent artifacts.
- Atomically adds the Waycloak chained entry after the existing network plugin
  and requests containerd's `cgroupPath` runtime capability.
- Preserves the exact prior conflist and can restore it byte-for-byte.
- Removes preview scheduling eligibility before upgrade, rollback, or uninstall.
- Refuses partial installation and reports a stable node condition.

### Chained CNI plugin

- Consumes the exact Pod UID, sandbox container ID, network namespace,
  `prevResult`, and cgroup-parent values from the runtime invocation.
- Confirms those values describe one admitted `ebpf-preview` Pod and one current
  UID-bound allocation; ambiguous or unavailable intent returns an error.
- Installs and pins default-deny at the Pod-parent cgroup before returning
  successful `ADD` or enabling any protected route.
- Preserves prior CNI results and unrelated interfaces, routes, qdiscs,
  programs, maps, and rules.
- Implements idempotent `ADD`, tolerant `DEL`, meaningful `CHECK`, and bounded
  stale-state `GC` semantics.

### Node agent

- Adopts only exact Waycloak-owned UID/generation pins and never path-prefix or
  alphabetical identities.
- Reconciles executable node capability and per-Pod observed health.
- Performs atomic deny-to-deny program replacement and map/generation updates.
- Treats missing, severed, unverifiable, or foreign links as not ready and
  keeps the workload closed.
- Garbage-collects only UID-owned stale pins after confirming Pod deletion and
  allocation quarantine state.
- Owns the selected preview feature subset's VXLAN, route, DNS translation, and
  drift-repair lifecycle without granting capabilities to application
  containers.

## Feature-subset rule

Preview selection is accepted only for behavior the node path implements and
passes through the backend-neutral conformance suite. Unsupported combinations
fail admission with a stable reason.

At minimum, the release must support ordinary TCP and UDP egress, contained DNS
over UDP and TCP, gateway replacement, tunnel/gateway loss, agent restart, and
all three cluster-traffic modes. A `PortForwardLease` targeting a preview
workload is rejected until gateway-to-Pod DNAT, Pod-local target translation,
renewable delivery, and any required unprivileged relay pass the same lease and
adapter contract. The release may omit that port-forward subset, but may not
partially enable it.

## Fail-closed lifecycle requirements

The selected path must remain closed during:

- scheduling and sandbox creation before CNI intent is available;
- CNI failure, timeout, duplicate `ADD`, or partial setup;
- node-agent absence, restart, upgrade, or loss of API connectivity;
- verifier rejection, pin failure, link severing, or failed program update;
- gateway startup, loss, replacement, and membership-generation change;
- allocation change, Pod restart/reschedule, rapid UID replacement, and CNI
  `DEL`/`GC` races;
- node reboot, CNI upgrade/rollback, and Waycloak uninstall.

No readiness condition, label, controller decision, or node-agent failure may
detach the last deny boundary before a replacement deny is observed attached.

## Security boundary

- Application containers receive no additional capabilities or host mounts.
- VPN credentials remain confined to the gateway engine.
- The node agent and installer receive only the host access demonstrated by the
  implementation; `privileged: true`, host PID, `CAP_SYS_ADMIN`, writable CNI
  directories, cgroupfs, and bpffs are separate threat-model decisions.
- The Ubuntu/Raspberry Pi path uses a narrow operator-installed AppArmor
  profile. `Unconfined` is diagnostic evidence only and is not releasable.
- Node compromise remains outside the Pod-level boundary, but the preview must
  document the increased node-wide blast radius relative to Pod-netns-scoped
  `NET_ADMIN`.

## Performance and footprint requirements

The comparison uses equivalent behavior and reports total node plus Pod cost.
It includes:

- current two-second sidecar reconciliation;
- a diff/event-driven nftables/netlink sidecar control;
- the complete preview node path;
- 1, 10, 50, and feasible stress-count workloads per node;
- startup and recovery latency, CPU, memory, container count, image pulls,
  throughput, latency, UDP loss, DNS, drift repair, and generation updates;
- separate amd64 and arm64 results with kernel, runtime, CNI, and LSM identity.

The preview must remove the two privileged networking init operations and the
long-running privileged networking sidecar from selected Pods, or demonstrate
another material measured benefit accepted in an ADR amendment. Moving equal or
greater cost into a DaemonSet is not a component-footprint success.

## Release acceptance

All items are required for `v0.4.0`:

1. Default-mode regression: the signed sidecar path remains unchanged and
   passes its existing unit, race, envtest, Kind/k3d, and real-provider gates.
2. Prepared-node lifecycle: install, executable probe, scheduling eligibility,
   upgrade, rollback, and safe uninstall pass on one amd64 and one arm64 node.
3. Creation-time identity: packet capture proves zero direct-egress packets
   before, during, and after CNI `ADD`, including missing intent and injected
   user init/native-sidecar containers.
4. Link lifecycle: loader/node-agent death, atomic update, failed update,
   controller loss, severed target, rapid recreation, stale-pin cleanup, and
   node reboot preserve the last deny boundary.
5. Complete declared feature subset: backend-neutral IPv4/IPv6, TCP/UDP, DNS,
   fragmentation and relevant GSO/GRO tests pass without touching unrelated CNI
   state.
6. Node-owner threat model: every capability, namespace entry, host mount, LSM
   rule, and RBAC permission is documented and tested; application containers
   remain capability-free.
7. Heterogeneous behavior: sidecar workloads run on ordinary nodes, preview
   workloads schedule only to eligible nodes, unsupported requests stay
   Pending or fail admission with stable reasons, and runtime capability loss
   fails closed without fallback.
8. Measured value: the equivalent performance/footprint report demonstrates
   release-worthy consolidation or another accepted material benefit on both
   architectures.
9. Packaging: signed multi-architecture artifacts, reproducible Helm output,
   SBOM/provenance/vulnerability policy, immutable references, and documented
   prepared-node rollback pass.
10. Homelab acceptance: the exact signed release is deployed with default and
    preview workloads concurrently; forced failure and recovery prove no direct
    egress, and cleanup leaves no research resources or stale pins.

## Non-goals

- Replacing the default sidecar backend.
- Transparent or load-based backend selection.
- Silent fallback from preview to sidecar.
- Claiming support for all Kubernetes runtimes, CNIs, kernels, or distributions.
- Shipping a host-veth tunnel/NAT rewrite before E2 is disproved.
- Removing application-specific adapters solely because networking moved to a
  node component.
- Product-wide observability, multi-gateway sharding, UI work, or additional VPN
  providers.

## Ordered implementation plan

| Order | Issue | Deliverable | Dependency gate |
| ---: | --- | --- | --- |
| 1 | [#107](https://github.com/Amoenus/waycloak/issues/107) | Preview API, status, conformance, and threat boundary | Research and PRD review |
| 2 | [#108](https://github.com/Amoenus/waycloak/issues/108) | Executable containerd CNI UID/netns/cgroup handoff proof | #107 |
| 3 | [#109](https://github.com/Amoenus/waycloak/issues/109) | Node capability plus link/pin/update/adoption/GC lifecycle | #108 |
| 4 | [#110](https://github.com/Amoenus/waycloak/issues/110) | Node-owned VXLAN, routing, DNS, health, and drift repair | #109; threat-boundary approval |
| 5 | [#111](https://github.com/Amoenus/waycloak/issues/111) | Explicit admission, scheduling, status, and no fallback | #107 and #109 |
| 6 | [#112](https://github.com/Amoenus/waycloak/issues/112) | Atomic prepared-node packaging, upgrade, rollback, uninstall | #108-#110 |
| 7 | [#113](https://github.com/Amoenus/waycloak/issues/113) | Equivalent default/tuned-default/preview measurements | Complete #110 path |
| 8 | [#114](https://github.com/Amoenus/waycloak/issues/114) | Signed mixed-mode homelab release certification | #107-#113 |

[Epic #6](https://github.com/Amoenus/waycloak/issues/6) owns the dependency
graph. Research issues [#65](https://github.com/Amoenus/waycloak/issues/65) and
[#34](https://github.com/Amoenus/waycloak/issues/34) remain the evidence record;
implementation findings that contradict the PRD reopen the relevant decision
instead of bypassing its gate.

## Cutoff rule

The preview is removed from the release rather than weakened if any required
fail-closed, ownership, rollback, heterogeneous-scheduling, or measured-value
gate remains unproved. `v0.4.0` may not describe kernel feature presence, a
successful loader probe, or deny-only disposable testing as production support.
