# ADR 0034: CNI creation-time enforcement and node-owned data plane

Status: Proposed
Date: 2026-07-26
Supersedes on acceptance: ADR 0002 mutation path, ADR 0005 startup handshake,
ADR 0006 Pod-local backend, and ADR 0024 preview-only CNI handoff

## Context

The alpha product injects privileged init/sidecar containers and waits for a
ConfigMap allocation. It proved the packet invariant, but makes admission
mutation, projected files, and a privileged component inside each Pod part of
the permanent workload contract. It is operationally heavy and couples data
plane upgrades to application rollout.

CNI is Kubernetes' creation-time network extension point. A chained plugin runs
before kubelet starts application containers and can fail Pod sandbox creation.
That is the correct lifecycle boundary for a guarantee that must exist before
the first application packet.

## Decision

The stable Core data plane is CNI-first and node owned.

For every Pod carrying the route label, the chained Waycloak CNI plugin:

1. resolves the exact Pod namespace, name and UID through the local node agent;
2. installs and durably records the deny-first boundary in the new network
   namespace before requesting any allocation;
3. waits a bounded time for an accepted route and a UID-scoped
   `VPNWorkloadBinding` allocation while the deny remains active;
4. asks the node agent to program the selected overlay/routing backend without
   removing the deny-first boundary;
5. verifies DNS, ordinary-egress denial and required gateway reachability; and
6. returns success only after the protected baseline is installed and observed.

Any unresolved intent, unavailable agent, incomplete allocation, unsupported
node, programming error, or verification failure makes CNI `ADD` fail. The Pod
sandbox does not become runnable. CNI `DEL` is idempotent and succeeds safely
with partial state; `CHECK`, `GC`, chained `prevResult`, rollback, runtime restart,
and stale namespace cleanup follow the CNI specification.

A privileged per-node agent owns ongoing nftables/netlink/eBPF state, node
capability reporting, drift repair and tunnel-loss enforcement. Application
Pods receive no Waycloak sidecars, init containers, host mounts, or Linux
capabilities. Backend technology may evolve behind the same CNI contract; eBPF
is not itself a public workload API.

Admission remains useful but is not the packet-security boundary:

- structural and object-local rules use CRD schemas and CEL;
- stable Kubernetes MutatingAdmissionPolicy may add static scheduling metadata
  on supported Kubernetes versions;
- a small webhook may perform dynamic reference/authorization checks or improve
  scheduling diagnostics where CEL cannot read arbitrary objects;
- CNI still refuses unsafe setup if admission is bypassed or unavailable.

The node agent exposes only a narrow authenticated local Unix-socket protocol.
It watches typed resources using least-privilege RBAC and never receives VPN
credentials. Gateway credentials stay in the gateway namespace.

## Consequences

- Fail-closed startup aligns with kubelet's network lifecycle rather than
  container startup ordering.
- Data-plane fixes no longer require injecting or restarting application
  containers.
- Installation requires CNI configuration and a privileged DaemonSet, so
  managed clusters that forbid chained CNI plugins are explicitly unsupported
  for stable Core rather than receiving a weaker fallback.
- Runtime/CNI compatibility and node upgrade testing become substantial release
  obligations.
- The existing sidecar backend is removed after cutover, not maintained as a
  second stable path.

## Alternatives rejected

- Keep sidecars as stable and CNI as preview: preserves the prototype contract
  and doubles the long-term test matrix.
- Admission-only mutation: admission cannot enforce packets after startup or
  when bypassed.
- NetworkPolicy alone: policy semantics and enforcement vary and do not provide
  Waycloak's gateway-specific readiness/allocation contract.
- Namespace-wide transparent interception: violates explicit workload opt-in.
- Require eBPF everywhere: unnecessarily narrows portability; the CNI contract
  permits tested nftables/netlink and eBPF implementations.

## Related decisions

- [ADR 0027](0027-explicit-workload-opt-in-and-attachment.md)
- [ADR 0029](0029-common-status-and-condition-contract.md)
- [ADR 0030](0030-capability-advertisement-and-conformance-profiles.md)
- [ADR 0033](0033-upstream-api-integration-boundary.md)
