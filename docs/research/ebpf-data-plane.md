# eBPF data-plane research for Waycloak v0.4.0

Status: Decision complete; derived PRD under review
Last updated: 2026-07-19
Governing issue: [#65](https://github.com/Amoenus/waycloak/issues/65)
Decision context: [ADR 0006](../decisions/0006-native-linux-data-plane.md),
[ADR 0019](../decisions/0019-optional-ebpf-data-plane.md)

## Executive finding

It is premature to define `v0.4.0` as a shipped eBPF backend.

The target nodes expose strong BPF capabilities, but capability availability is
not the hard part. The hard part is selecting an ownership and attachment model
that is installed before application traffic, remains effective through loader
and controller failure, covers every packet class in Waycloak's contract,
coexists with the CNI, and has safe upgrade and garbage-collection semantics.

The current nftables rule is not the whole data plane. The Pod-local agent also
owns VXLAN creation, overlay addressing, policy routing, DNS destination NAT,
and application-port destination NAT. A filter-only eBPF replacement would keep
those netlink and netfilter responsibilities and retain per-Pod `CAP_NET_ADMIN`,
while adding BPF loading and attachment privilege. It therefore has no proven
privilege or component-footprint benefit yet, although it may still have
performance or scaling value that should be measured.

The leading research direction is a node-owned, Pod-identity-aware enforcement
model, with cgroup egress and host-veth tc/TCX as attachment candidates. This
could remove injected Waycloak networking containers and their privilege from
protected Pods. It would not remove application-specific lease adapters such as
the qBitTorrent adapter unless their separate application contract also moves.
A node model changes Waycloak's trust boundary into a privileged node agent and
creates identity, startup-order, CNI, persistence, upgrade, and cleanup risks.

## Value hypotheses

The research scores four independent outcomes. A design need not achieve all
four, but any supported second backend must demonstrate enough measured value
to justify its additional security and maintenance surface.

1. **Privilege:** remove or narrow per-Pod `CAP_NET_ADMIN`, without moving an
   unjustifiably broad capability set into a node component.
2. **Component footprint:** remove or consolidate the two gating init containers
   and restartable Waycloak agent currently injected into each protected Pod.
3. **Performance and scale:** improve aggregate CPU, memory, startup,
   reconciliation, throughput, latency, UDP loss, or workload-count scaling.
4. **Operations:** improve ownership inspection, drift repair, upgrades,
   compatibility reporting, or failure diagnosis.

Moving work from Pods to a DaemonSet is not a free reduction. Measurements must
report the total node and per-workload cost, not only the protected Pod's
container count or resource requests.

## Compatibility and selection model

The current Pod-local nftables/netlink mode remains the portable, supported
default. eBPF is researched as an additional mode for nodes that operators have
intentionally prepared and that Waycloak has verified with executable
capability probes. A heterogeneous cluster may therefore run the default mode
on ordinary nodes and eBPF mode only on eligible nodes.

Selection must be explicit in desired Kubernetes state and observable in
admitted Pod/status state. If eBPF is selected, an unsupported node must prevent
scheduling or application startup with a stable reason. Waycloak must not react
to load, attachment, verifier, or runtime failure by silently switching the
workload to the sidecar backend; that would make the protection mechanism differ
from the operator's declared and tested choice.

This model does not require all users to prepare every node for eBPF. It does
require a documented node-enablement procedure, per-node capability conditions,
and scheduling semantics for users who choose the optional mode.

Node preparation is an operator choice, not a cluster-wide prerequisite. The
capability contract must therefore work in mixed clusters: an ordinary node is
eligible for the portable sidecar mode, while an eBPF-selected workload is
eligible only for a node whose preparation label and observed capability
condition both match the requested mode. A label alone is not evidence. Loss of
the observed capability after scheduling must make the selected path not ready
and fail closed; it must not trigger a transparent switch to sidecar mode.

Kubernetes required node affinity is `IgnoredDuringExecution`: it prevents
initial placement when a label is absent, but a running Pod is not evicted or
stopped merely because that label later changes. Admission should therefore add
hard affinity for initial placement, using a protected administrator-controlled
preparation label plus a Waycloak-observed capability label, but the node data
plane must independently enforce and report runtime health. The controller must
not treat scheduling metadata as a live enforcement signal. Where NodeRestriction
is enabled, the administrator-intent key should use the reserved
`node-restriction.kubernetes.io` segment so a kubelet cannot self-declare the
trust prerequisite. Waycloak can optionally interoperate with Node Feature
Discovery, but cannot require it; NFD labels are feature advertisements rather
than executable proof of the complete Waycloak attachment contract.

## Product invariants used as evaluation criteria

- Opt-in is explicit on the workload Pod template.
- A protected workload never silently uses ordinary internet egress during
  startup, tunnel or gateway loss, agent loss, reconfiguration, replacement,
  upgrade, drift, or node-component failure.
- Application containers receive no additional Linux capabilities.
- Credentials remain confined to the VPN engine container.
- Stable membership and lease identities are never inferred from ordering.
- `Ready` describes observed data-plane health.
- Waycloak does not replace or flush CNI-owned or unrelated state.
- Unsupported capability fails explicitly before application startup; there is
  no runtime backend fallback.
- Externally visible behavior is proved with packet-level acceptance tests.

## As-built responsibility and component map

The existing `dataplane.Backend` seam has five operations: `Preflight`,
`InstallLockdown`, `Configure`, `Verify`, and `Repair`. `Agent.Prepare` installs
lockdown before validating the remaining configuration. That ordering is the
security boundary an alternative must preserve.

Each protected Pod currently receives `waycloak-prepare` and
`waycloak-verify` gates plus a restartable `waycloak-agent`. The injected
containers have `CAP_NET_ADMIN`; applications and lease adapters do not.

| Responsibility | Current mechanism | Can filter eBPF replace it? |
|---|---|---|
| Initial deny | Pod-netns nftables output chain with drop policy | Yes, if attachment precedes all relevant traffic and persists |
| Allowed underlay tunnel packet | nftables output match | Yes |
| Cluster-traffic modes | nftables prefix verdicts plus policy rules | Filtering yes; routing no |
| VXLAN device and overlay address | netlink link/address operations | No |
| Protected default and exception routes | netlink routes and rules | No, unless the architecture changes |
| DNS interception | nftables output DNAT plus policy routing | Unproved; rewrite and route timing need tests |
| Lease target-port translation | nftables prerouting DNAT | Unproved; ingress, checksum, and conntrack behavior need tests |
| Gateway health | userspace probe through the protected route | No need |
| Drift repair | nftables/netlink reconciliation | Needs new program/link/map ownership verification |

This means a Pod-local filter swap leaves the existing agent and most of its
privilege in place. Removing the Waycloak sidecar requires moving routing,
overlay, NAT, verification, and repair ownership—not only packet filtering—or
redesigning the data path so those Pod-netns objects no longer exist.

### Sidecar-reduction boundary

Kubernetes init containers are a genuine startup barrier: application
containers do not start until ordinary init containers complete. Readiness
conditions and probes are not an equivalent egress barrier; they control
Service endpoint participation and health reactions while the application
process is already running.

This produces three distinct component outcomes:

1. A Pod-local eBPF backend probably retains the existing gates and agent.
2. A node-owned backend may reduce two privileged gates plus the long-running
   agent to one small, unprivileged init gate that waits for observed,
   UID/generation-bound node enforcement before allowing the application to
   start. The delivery mechanism for that observation still needs design.
3. Zero injected Waycloak networking containers requires enforcement during
   sandbox/network creation, a pre-existing node-wide deny mechanism with a
   race-free identity policy, or equivalent runtime integration. Merely watching
   Pods from a DaemonSet leaves a creation-to-attachment race. CNI chaining is a
   possible setup-time hook, but it expands installation and CNI compatibility
   scope and cannot become a dependency of the default sidecar mode.

These options must be benchmarked separately. “Sidecarless” must never mean
that readiness replaces the deny-before-application startup barrier.

## Kernel mechanism findings

### Loading, privilege, and attachment are separate

The kernel verifies programs at `BPF_PROG_LOAD`; a successful load returns a
file descriptor, while verifier rejection is a normal compatibility outcome.
Since Linux 5.8, `CAP_BPF` separates privileged BPF operations from the
historically overloaded `CAP_SYS_ADMIN`, but networking attachment can still
require `CAP_NET_ADMIN`. Both sampled nodes disable unprivileged `bpf()`, so an
intended loader needs explicit privilege. Kubernetes seccomp, AppArmor/SELinux,
user namespaces, mounts, and Pod Security remain part of the capability test.

Preflight must therefore attempt the exact load and attachment under the
intended container security context and retain bounded verifier diagnostics.
Kernel version, config flags, or BTF presence alone cannot declare support.

### Object lifetime must be designed

Closing the final program or map descriptor normally destroys the object.
Pinning an object in bpffs retains a reference beyond process lifetime.
`BPF_LINK_CREATE` returns a link descriptor, and `BPF_LINK_UPDATE` can replace
its program. These mechanisms are promising for crash survival and atomic
replacement, but the selected hook must prove pinning, detach, failed update,
node reboot, and stale-object cleanup on every supported kernel.

### Candidate hooks have different coverage

- `BPF_PROG_TYPE_CGROUP_SKB` supports cgroup ingress and egress and avoids
  interface-name coupling. A node component must reliably find and own the
  Pod-level cgroup. Socket connect hooks alone are insufficient because
  Waycloak covers UDP and packets, not only connected TCP sockets.
- `BPF_PROG_TYPE_SCHED_CLS` at tc ingress/egress sees a specific interface. In
  the Pod netns it can approximate the existing output boundary; on the
  host-side veth, Pod egress appears on host ingress. Classic tc and TCX link
  attachment have different lifecycle/ownership behavior and must compose with
  rather than displace CNI programs or qdiscs.
- XDP runs on a device receive path and is not a natural standalone Pod-output
  boundary. LWT programs depend on routing state and do not provide an
  independent deny-before-route gate.
- Socket address hooks can redirect selected connect/send operations but are
  not complete packet enforcement. They may help DNS only after exact TCP/UDP
  and connected/unconnected coverage is demonstrated.

The live `bpftool feature probe kernel` helper inventory on both representative
nodes narrows the cgroup option further. `cgroup_skb` can read packet and socket
identity and use maps/ring buffers, but neither sample exposes packet-store,
L3/L4 checksum-rewrite, redirect, clone-redirect, or tunnel-key helpers for that
program type. The attachment is therefore a viable allow/drop and observation
gate, not a DNS/port NAT, routing, or tunnel implementation on the target
support boundary.

### Portability is executable evidence

BTF and CO-RE relocate compiled programs against running-kernel types, and
`/sys/kernel/btf/vmlinux` exists on both samples. This reduces build coupling;
it does not guarantee hook, helper, map, verifier, security-policy, or
architecture compatibility. A Go loader can evaluate the MIT-licensed
`cilium/ebpf` library for loading, links, BTF, pinning, feature probes, and
`bpf2go`, while keeping program-source licensing explicit.

## Target homelab evidence

Collected on 2026-07-19 using Kubernetes read-only inventory and short-lived
privileged probe Pods. The probes mounted the host root read-only, emitted only
kernel/networking metadata, carried no credentials, and were deleted.

| Property | amd64 sample | arm64 sample |
|---|---|---|
| Node/OS | Flatcar 4593.2.4 | Ubuntu 26.04 Raspberry Pi |
| Kernel | 6.12.95-flatcar | 7.0.0-1014-raspi |
| Runtime/CNI | containerd 2.2.3-k3s1 / Flannel VXLAN | same |
| Cgroup | v2 | v2 |
| vmlinux BTF | present | present |
| bpffs | mounted read-write at `/sys/fs/bpf` | same |
| Unprivileged BPF | disabled (`2`) | disabled (`2`) |
| JIT | enabled and always on | enabled and always on |
| Feature probe | sched-cls, XDP, cgroup-skb, LWT out/xmit | same |
| Selected maps | hash, array, LPM trie, ring buffer | same |
| Security lockdown | none selected | none selected |
| Sample container LSM context | AppArmor unavailable; SELinux-style `kernel_t` process context observed | AppArmor `cri-containerd.apparmor.d` enforced |

The sampled Flannel host had `cni0`, `flannel.1`, and per-Pod veth devices. It
reported no existing XDP, tc, flow-dissector, or netfilter BPF attachments and
no filters on `cni0` or `flannel.1`. This is an initial-probe opportunity, not a
portable assumption about other CNIs or future Flannel versions.

The live qBitTorrent Pod maps to a systemd cgroup-v2 Pod slice whose name embeds
the Kubernetes Pod UID with underscores. Its sandbox and three running
containers are separate `cri-containerd-*.scope` children of that Pod slice.
This gives a concrete stable-identity join candidate for a node loader. It does
not yet prove that one parent `cgroup_skb` attachment has the required effective
coverage or that the slice appears early enough to close startup traffic.

Isolated amd64 and arm64 deny-only probes then attached the same two-instruction
`BPF_PROG_TYPE_CGROUP_SKB` egress program to disposable Pods' UID-bound parent
cgroups. Each link was pinned in bpffs, its loader process and Pod exited, and
the target could no longer connect to the Kubernetes API Service. Removing the
exact pin detached the program and connectivity returned immediately. Every
target, loader, source ConfigMap, cleanup Pod, and pin was deleted.

This proves parent-cgroup attachment, link pinning across loader exit, and
explicit detach/recovery for one BestEffort Pod on each target architecture. It
does not yet prove init/sibling coverage, application-start ordering, complete
IPv4/IPv6 protocol coverage, atomic replacement, or node reboot. The probes also
showed that the systemd path includes Kubernetes QoS (`besteffort` or
`burstable`), so a loader must discover and validate the UID-bound parent rather
than construct one fixed QoS path.

Reduced-capability allow-only probes produced a second portability finding. On
amd64 Flatcar, `CAP_BPF` plus `CAP_NET_ADMIN`, all other capabilities dropped,
`allowPrivilegeEscalation: false`, and `RuntimeDefault` seccomp successfully
loaded, attached, pinned, unpinned, and closed the cgroup link without
`privileged: true`. The same shape on arm64 Ubuntu loaded and attached but could
not pin because `cri-containerd.apparmor.d` was enforced. An A/B rerun changed
only the container AppArmor profile to `Unconfined` and pin/unpin succeeded.

This shows that the tested path does not inherently require a fully privileged
container, but `BPF` plus `NET_ADMIN` has not yet been proved minimal. It also
shows that the Raspberry Pi kernel is BPF-capable while the default runtime LSM
policy is not sufficient for persistent links. A supported node-enablement path
should install a narrow Waycloak AppArmor profile permitting the required BPF
and bpffs operations; `Unconfined` is diagnostic evidence, not the preferred
production policy. Executable preflight must exercise pinning under that exact
profile.

### Parent coverage and link-update lifecycle evidence

A two-phase disposable Pod proved parent-cgroup coverage across Kubernetes
container roles. During initialization, one parent attachment denied egress
from both a restartable init sidecar and a concurrently running ordinary init
container. After explicit unpin and release of the init gate, a new parent
attachment denied both the application container and the same restartable
sidecar. Every container recovered after unpin. This establishes effective
coverage of separate containerd child scopes under the sampled Pod parent.

A separate amd64 update probe exercised a pinned link without detaching it:

- a wrong-program-type update was rejected and the existing deny remained;
- a valid deny-to-deny update completed while 12 consecutive target connection
  attempts observed zero successes;
- a deny-to-allow update restored connectivity through the same link;
- a subsequent allow-to-deny update blocked it again;
- explicit unpin and close restored connectivity.

The bounded 12-attempt result is mechanism evidence, not a statistical claim of
zero possible packet windows. The backend conformance suite still needs a
higher-rate packet capture/count assertion around update and rollback.

An earlier harness timeout produced useful teardown evidence: when the target
Pod completed and its cgroup disappeared, the kernel reported the pinned link as
severed and rejected a later update. The bpffs pin remained until explicit
cleanup. A node owner must therefore treat a severed target as fail-closed for
status purposes and garbage-collect the exact UID-owned stale pin; pinning alone
does not solve lifecycle ownership.

## Candidate architecture matrix

Ratings are current inferences, not an ADR decision.

| Candidate | Fail-closed lifecycle | Privilege/components | Completeness | Current judgment |
|---|---|---|---|---|
| Pod-local tc/TCX filter; retain netlink/nftables NAT | Potentially equivalent if first and persistent | Keeps agent/init gates and `NET_ADMIN`; adds BPF | Still needs VXLAN/routes/NAT | Low component value; benchmark/control candidate |
| Pod-local cgroup attachment | Parent coverage is proved, but attachment still occurs from an injected component | Keeps gates, agent, netns work, and per-Pod privilege; adds BPF/bpffs | Routing/NAT remain | Reject as shipped mode; useful control only |
| Node agent + Pod-cgroup egress deny | Pinned deny survived loader exit on amd64 and arm64; discovery alone has a pre-start race | Could remove Pod filter privilege; adds privileged DaemonSet | Routing/NAT still need ownership | Viable only with a pre-container handoff |
| Chained CNI `ADD` + Pod-cgroup deny + node owner | Runtime ordering can install deny before containers; exact containerd handoff still needs executable proof | Highest sidecar-reduction potential; adds CNI installation and node privilege | Initial setup is completeable; dynamic routing/NAT ownership remains | Leading prototype candidate (E2) |
| Node agent + host-veth tc/TCX | Can cover Pod packets after veth discovery; creation race unresolved | Could remove Pod filter component; adds node agent | Routing/NAT still separate | Promising, more CNI-coupled |
| Node agent owns complete tunnel/routing/filter/NAT path | Could centralize the boundary | Can remove Waycloak networking containers; broad node privilege | Potentially complete | High value and high risk; effectively CNI-like |
| XDP-only or socket-hook-only | Incomplete packet/lifecycle coverage | Varies | Misses required traffic classes | Reject standalone |

Application-specific lease adapters are orthogonal. A node data plane can
remove `waycloak-agent` without removing the qBitTorrent adapter's need to call
qBitTorrent APIs when an externally advertised port/address changes.

## Ownership of routing, NAT, and injected components

The cgroup probe establishes a strong fail-closed primitive but does not satisfy
the existing `dataplane.Backend` contract by itself. Four ownership models
remain:

### E0: Pod-local hybrid

Keep the existing injected gates/agent, netlink VXLAN/routes, and nftables DNS
and lease DNAT. Replace only the output allow/drop chain with cgroup eBPF.

- Lowest architectural change and easiest backend-conformance comparison.
- Does not reduce injected components or per-Pod `NET_ADMIN`; adds `BPF` and a
  cgroup/bpffs attachment surface.
- Adoption is justified only by measured packet/rule-management performance or
  materially better observation/drift repair.

### E1: Node cgroup gate plus Pod-netns manager

A node DaemonSet owns cgroup programs and enters each protected Pod's network
namespace to retain the existing VXLAN, route, nftables NAT, verification, and
repair behavior.

- Could reduce protected Pods to one unprivileged startup gate and no
  long-running Waycloak networking sidecar.
- Joining another network namespace requires `CAP_SYS_ADMIN` in the relevant
  user namespaces; host PID/netns discovery and host-path access create a much
  larger node-level blast radius than today's Pod-netns-scoped `NET_ADMIN`.
- Moving the same operations is not automatically a security improvement and
  requires an explicit threat-model decision.

### E2: CNI-time setup plus node owner

An optional chained CNI component establishes the deny and networking state
during Pod sandbox creation; a node agent adopts and repairs it.

- Provides the strongest route to zero injected Waycloak networking containers
  because setup precedes application container creation.
- Mutates node CNI binaries/configuration and becomes runtime/CNI-version
  sensitive. Installation, upgrades, rollback, ordering, and coexistence are
  materially broader than the default Helm-only Pod-local mode.
- May be acceptable only as an explicitly prepared-node integration; it cannot
  become a hard dependency for ordinary Waycloak installations.

The CNI specification gives this model a real startup boundary: the runtime
creates the network namespace before invoking plugins, invokes a chained list in
order, and halts `ADD` on an error. A Waycloak chained plugin can therefore
install Pod-netns deny state before the runtime proceeds. It also receives the
network namespace and container identity and has defined `DEL`, `CHECK`, and
`GC` operations. Those semantics are stronger than discovering a veth or cgroup
after containers have begun, but they do not solve Kubernetes intent lookup,
Pod UID joining, pinned-link ownership, or runtime-specific installation. The
plugin must be idempotent, preserve `prevResult`, make `DEL` tolerate missing
namespaces, and have an independent stale-state reconciler because catastrophic
node loss can omit normal cleanup.

### E3: Host-veth tc/TCX tunnel and translation

A node agent uses the cgroup gate for default deny, then a host-veth tc/TCX
program plus node-owned tunnel/maps to route, translate, and return protected
traffic without configuring the Pod netns.

- Could remove Waycloak networking containers and avoid per-Pod namespace
  entry.
- Reimplements routing, encapsulation, DNS translation, lease translation,
  reverse-path behavior, checksums, fragmentation, offload, MTU, and stateful
  flow handling in a CNI-like data plane.
- Has the highest implementation, verifier, CNI-ordering, and conformance risk;
  it is not a small backend substitution.

Current evidence rejects describing any sidecarless option as merely “switching
the backend.” E0 is the only direct backend swap. E1-E3 are new ownership
architectures and must earn adoption through component/performance value and an
accepted threat-model change.

### Target-runtime CNI handoff finding

The exact containerd 2.2.3 source used by the homelab materially strengthens E2.
Before CNI setup, containerd has the sandbox network-namespace path. Its CRI
adapter passes `K8S_POD_NAMESPACE`, `K8S_POD_NAME`, the infra-container ID, and
`K8S_POD_UID` as CNI labels. When the CRI sandbox configuration contains a
cgroup parent, it also supplies that value through go-cni's `cgroupPath`
runtime capability. The chained plugin can therefore receive all three
identities needed at the creation boundary: exact Pod UID, target netns, and
Pod-parent cgroup.

This is a containerd integration, not a portable CNI guarantee. The CNI
conventions document dynamic capability negotiation but do not list
`cgroupPath` among the well-known cross-runtime capabilities. Other runtimes
need separate adapters or must be declared unsupported. The target k3s node's
read-only configuration inspection found a single CNI 1.0.0 Flannel conflist
with `flannel`, `portmap`, and `bandwidth`; no cgroup consumer is currently
configured. Prepared-node installation must atomically add a Waycloak chained
entry requesting `cgroupPath`, install a matching architecture-specific binary,
validate the actual runtime payload, and restore the exact prior conflist on
rollback.

## Performance and footprint study

### Initial homelab baseline

A short metrics-server observation on 2026-07-19 sampled the two protected
workloads currently running in the homelab ten times over approximately 20
seconds. The Bitmagnet `waycloak-agent` reported 34-42 millicores and 12-13 MiB;
the qBittorrent `waycloak-agent` reported 43-48 millicores and 12-13 MiB. The
application-specific adapters were separate consumers and are not removable by
a networking-backend change alone.

This is an observed baseline snapshot, not a benchmark: metrics-server values
use a collection window, the workloads were not load-controlled, and the
samples are correlated. It is nevertheless large enough to justify a controlled
study. The as-built agent has two independent two-second loops. One refreshes
lease delivery, while the other reloads allocation state and calls `Repair`;
`Repair` reruns full configuration, replaces nftables policy, verifies link,
address and route state, and performs a new non-keepalive gateway health request.
The benchmark must distinguish the cost of this reconciliation design from the
cost of nftables packet processing. An event-driven or diff-based sidecar could
capture some benefit without eBPF and is therefore a required comparison.

Measurements must compare equivalent fail-closed behavior, not a reduced eBPF
feature set. At minimum:

- 1, 10, 50, and a feasible stress count of protected Pods per node;
- total node CPU/memory plus controller, node-agent, and per-Pod contribution;
- injected container count, image pulls, startup latency, and kubelet/runtime
  object overhead;
- steady-state reconciliation and forced-drift repair cost;
- TCP throughput/latency, DNS UDP/TCP latency, and sustained UDP loss;
- gateway-loss detection, enforcement, and recovery latency;
- program/rule update cost during membership and endpoint change;
- amd64 and arm64 results with kernel/CNI/security context recorded.

The controlled comparison must include at least: the current two-second
sidecar; a tuned event-driven/diff-based sidecar retaining the same nftables and
netlink contract; and any eBPF prototype with equivalent behavior. This prevents
ordinary users on non-eBPF-ready nodes from being left with avoidable overhead
and prevents a reconciliation optimization from being misattributed to eBPF.

### Ten-workload real-provider burst

A disposable Deployment pinned to one amd64 node was admitted through the
released sidecar path and scaled from one to ten protected Pods. The first Pod
reached `Initialized` 11 seconds after creation and `Ready` after 15 seconds.
Scaling from one to ten reported rollout completion after 129 seconds. The nine
new Pods were created within one second; their initialization completed 38-73
seconds after creation and their first recorded `Ready` transitions occurred
113-128 seconds after creation.

Available replicas repeatedly rose and fell during the burst. Agent logs showed
gateway-health HTTP 503 and timeout failures while preserving existing deny
state. The gateway manager recorded five Gluetun loopback health failures
(`EOF` or a closed idle connection) during the observation window. Once the
provider health stabilized, all ten agents returned Ready without restart. This
is useful system evidence: the current contract failed closed and recovered, but
real-provider health churn dominated rollout latency. It is not valid evidence
of nftables-versus-eBPF packet performance.

The 50-workload real-provider step was deferred rather than amplifying a
provider/control-plane transient and falsely attributing global readiness churn
to either kernel backend. It should be rerun only as a deliberate system test of
membership-burst blast radius and recovery, separately from deterministic
backend performance.

Cleanup removed the Deployment and all Pods. Their `VPNWorkload` objects
immediately stopped contributing to desired gateway membership, while the
objects themselves entered the intentional five-minute allocation-quarantine
finalizer period. The gateway desired and applied membership generations both
returned to the original two-workload digest before quarantine expired. Object
retention is therefore address-reuse protection, not stale data-plane intent.

### Deterministic healthy-repair baseline

The existing fake-gateway end-to-end harness now contains a reproducible
`BenchmarkRepairHealthy`. It verifies an already prepared path and then runs the
complete current `Repair` operation: netlink configuration, nftables policy
replacement, state verification, and a gateway health request over the VXLAN.
Each architecture ran three samples of 20 repairs:

| Node sample | Per-repair samples | Median | Range |
| --- | ---: | ---: | ---: |
| amd64, Intel N95 | 49.64, 45.38, 36.68 ms | 45.38 ms | 36.68-49.64 ms |
| arm64, Raspberry Pi | 7.57, 7.20, 7.56 ms | 7.56 ms | 7.20-7.57 ms |

Both runs also passed the surrounding VXLAN, DNS, cluster-traffic,
drift-repair, agent-exit, and gateway-loss fail-closed tests. These are
wall-clock `ns/op` results, not CPU profiles or packet-throughput measurements;
they exclude allocation-file loading and the separate delivery-refresh loop.
They show that a two-second full-repair loop has measurable and strongly
platform-dependent work. They do not show that eBPF is faster. A filter-only
eBPF program would leave most measured operations intact, while an event-driven
or diff-based default agent could avoid many of them without changing the
packet backend.

A sidecarless design must also measure DaemonSet availability, node upgrade
ordering, and blast radius. A filter-only design must show performance gains
large enough to justify keeping both the existing Pod components and a second
kernel implementation.

## Critical unknowns and minimum probes

1. Extend the observed k3s/containerd Pod-parent/container-child cgroup mapping
   with creation timing, UID reuse, and teardown order; never assume one QoS
   path. Effective child-scope coverage is proved for the sampled container
   roles.
2. Extend the successful amd64/arm64 parent-cgroup deny/pin probes and compare a
   host-veth attachment; test connected and unconnected UDP, TCP, IPv4, and
   IPv6. Init/app/restartable-sidecar child coverage is proved on amd64.
3. Instrument a disposable chained plugin on the target k3s/containerd version
   to prove the source-observed `K8S_POD_UID`, `CNI_NETNS`, container ID, and
   `runtimeConfig.cgroupPath` values refer to the same sandbox. Extend that join
   across restart, rescheduling, rapid deletion, and identifier reuse.
4. Extend the proved loader-exit persistence with controller loss and node-agent
   restart adoption of an existing pinned link.
5. Extend the successful wrong-type rejection and deny-to-deny `BPF_LINK_UPDATE`
   probe with generation ownership, high-rate packet capture, and rollback.
6. Decide whether DNS and lease NAT remain nftables-hybrid or move; test TCP/UDP,
   checksums, fragments, GSO/GRO, conntrack, reverse traffic, and rotation.
7. Compose with a pre-existing program at the selected hook and prove ordering,
   inspection, and cleanup without touching CNI ownership.
8. Load/attach under the exact proposed capabilities, seccomp, LSM, mounts, and
   filesystem shape. Narrow the currently sufficient `BPF` + `NET_ADMIN` pair,
   define a least-privilege AppArmor policy for persistent links, and document
   any node-agent threat-model change.
9. Reboot a node and prove enforcement is re-established before protected Pods
   can resume, with safe severed-link and stale-pin reconciliation.
10. Run the performance/footprint matrix above before claiming value.

11. Prove atomic CNI binary/conflist installation, kubelet/containerd restart
    behavior, rollback to the byte-identical prior chain, and coexistence with
    Flannel, portmap, bandwidth, and an unrelated chained plugin.

## Research recommendation

Select the PRD decision gate's **prototype release** outcome and prototype E2:
an optional, prepared-node chained CNI handoff plus a node-owned Pod-cgroup eBPF
deny boundary. Do not accept an eBPF backend as production-supported yet.

This choice is evidence-based:

- cgroup egress deny covers all sampled container child scopes, persists after
  loader exit when pinned, updates without replacing the link, and works on the
  representative amd64 and arm64 kernels;
- containerd's creation path can hand the chained plugin the Pod UID, netns, and
  cgroup parent before successful network `ADD` permits sandbox startup;
- the current default's per-workload full-repair loop has measurable cost, so a
  sidecarless node owner has a plausible footprint/performance hypothesis;
- E0 cannot remove current privilege or components, E1 discovery alone leaves a
  startup race, and E3 requires a new tunnel/NAT data plane before its value is
  established.

The prototype must start deny-only and use disposable workloads. The CNI plugin
installs and pins the UID-owned cgroup deny before returning success, or returns
an error and leaves the sandbox unusable. A node agent adopts the exact pinned
generation, publishes executable node capability and per-Pod observed health,
performs atomic program updates, and garbage-collects severed UID-owned links.
The existing sidecar backend remains the default and comparison control.

The principal unresolved architectural gate is not eBPF attachment. It is who
owns VXLAN, routes, DNS DNAT, and lease DNAT after removing the sidecar. The node
agent can enter a Pod netns, but doing so requires a broader `CAP_SYS_ADMIN`
boundary. The PRD must require a threat-model decision and a prototype of that
ownership before claiming sidecar reduction. Until equivalent full-path tests
pass, eBPF mode is developer-preview only, rejects port-forward workloads if
their dynamic translation path is absent, and carries no performance or
production-support claim.

### Rejected release directions

- **Ship E0 as the eBPF mode:** rejected because it retains every injected
  networking component and `NET_ADMIN`, adds BPF privilege, and has no measured
  packet-path advantage.
- **Node discovery without CNI handoff:** rejected because observation after
  sandbox creation cannot prove deny-before-first-packet.
- **Make eBPF the default or silently fall back:** rejected by the heterogeneous
  compatibility model and explicit-backend invariant.
- **Implement E3 first:** deferred because complete host-veth tunneling and
  translation is a CNI-scale rewrite without evidence that simpler E2 ownership
  is insufficient.

No probe may carry production workload traffic until deny-only disposable tests
pass. Real-provider regression follows only after selecting the architecture.

## PRD decision gate outcome

The research gate allowed one of three evidence-backed outcomes:

1. **Adopt:** a specific architecture closes the contract and demonstrates
   sufficient privilege, footprint, performance, or operational value.
2. **Prototype release:** a promising architecture still lacks lifecycle or
   compatibility evidence; expose it as experimental with no production claim.
3. **Do not adopt:** no design justifies its trust and maintenance cost; select
   a different release outcome rather than shipping eBPF for its own sake.

The research selected outcome 2. The derived `v0.4.0` PRD therefore defines a
narrow developer preview, not a production-supported backend. It can ship only
after its implementation gates prove complete ownership, fail-closed behavior,
safe prepared-node lifecycle, heterogeneous operation, and material value.

## Primary-source evidence ledger

- Linux kernel [eBPF syscall reference](https://docs.kernel.org/userspace-api/ebpf/syscall.html):
  descriptor lifetime, pinning, cgroup attachment, links, and link update.
- Linux kernel [program type table](https://docs.kernel.org/bpf/libbpf/program_types.html):
  cgroup-skb, tc/TCX, XDP, LWT, and socket attachment classifications.
- Linux kernel [BTF documentation](https://docs.kernel.org/bpf/btf.html) and
  [libbpf overview](https://docs.kernel.org/6.10/bpf/libbpf/libbpf_overview.html):
  BTF encoding and CO-RE relocation.
- Linux kernel [program-run documentation](https://docs.kernel.org/bpf/bpf_prog_run.html):
  synthetic execution support and its boundary from live attachment tests.
- Linux man-pages [bpf(2)](https://man7.org/linux/man-pages/man2/bpf.2.html),
  [capabilities(7)](https://man7.org/linux/man-pages/man7/capabilities.7.html),
  and [tc-bpf(8)](https://man7.org/linux/man-pages/man8/tc-bpf.8.html): verifier
  diagnostics, `CAP_BPF`, and tc classifier behavior.
- Kubernetes [kernel security constraints](https://kubernetes.io/docs/concepts/security/linux-kernel-security-constraints/),
  [security context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/),
  [seccomp](https://kubernetes.io/docs/reference/node/seccomp/),
  [init containers](https://kubernetes.io/docs/concepts/workloads/pods/init-containers/),
  [probes](https://kubernetes.io/docs/concepts/workloads/pods/probes/), and
  [network plugins](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/network-plugins/):
  container capability/syscall boundaries, startup ordering, readiness scope,
  and the runtime-owned CNI integration surface.
- Kubernetes [assigning Pods to nodes](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/)
  and [NodeRestriction admission](https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/#noderestriction):
  hard initial node affinity, its ignored-during-execution limitation, and
  administrator-controlled isolation labels.
- The CNI project [specification](https://www.cni.dev/docs/spec/): network
  namespace creation and chained `ADD` ordering, error handling, `prevResult`,
  attachment identity, deletion, checking, and garbage collection.
- The CNI project [extension conventions](https://www.cni.dev/docs/conventions/):
  explicit runtime-capability negotiation and the boundary between well-known
  capabilities and runtime/plugin-specific extensions.
- containerd 2.2.3 [CRI sandbox CNI setup](https://github.com/containerd/containerd/blob/v2.2.3/internal/cri/server/sandbox_run.go)
  and its go-cni 1.1.13 [`cgroupPath` option](https://github.com/containerd/go-cni/blob/v1.1.13/namespace_opts.go):
  Pod UID labels, network-namespace setup, cgroup-parent capability handoff, and
  error propagation on target-runtime network setup.
- Kubernetes SIGs [Node Feature Discovery](https://kubernetes-sigs.github.io/node-feature-discovery/master/get-started/introduction.html)
  and its [customization guide](https://kubernetes-sigs.github.io/node-feature-discovery/master/usage/customization-guide.html):
  optional node feature advertisement and custom labeling integration.
- Cilium project [ebpf-go](https://github.com/cilium/ebpf): Go loading, BTF,
  link, pin, feature-probe, and code-generation surfaces and amd64/arm64 scope.

## Research integrity notes

- Documentation establishes mechanism semantics, not Waycloak compatibility.
- Homelab probes establish only sampled nodes and current CNI state.
- Candidate ratings are explicit inferences from primary sources and as-built
  code.
- Support requires packet/lifecycle evidence, not kernel version, config flags,
  BTF presence, or a successful synthetic program run.
