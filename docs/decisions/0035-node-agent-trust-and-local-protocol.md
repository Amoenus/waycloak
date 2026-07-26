# ADR 0035: Node-agent trust boundary and authenticated local protocol

Status: Accepted
Date: 2026-07-26

## Context

ADR 0034 moves continuing network ownership from privileged containers in each
application Pod to one privileged component per node. This removes application
Pod privilege but increases the node-wide blast radius. The CNI plugin and node
agent also need a local coordination channel during sandbox creation, when any
ambiguous identity or permissive error can violate the fail-closed guarantee.

The issue #124 fixture used an unauthenticated mode-0600 Unix socket. Filesystem
permissions exclude ordinary users, but do not authenticate responses after a
socket-path substitution, bind a response to one request, or reject replay.
The production boundary needs an explicit protocol and privilege budget before
replacement APIs or a production node agent are implemented.

## Decision

The CNI-to-node-agent boundary is `networking.waycloak.io/cni-node/v1` over a
root-owned Unix socket. It is not exposed through a host TCP port, Pod Service,
or workload mount.

Authentication has two layers:

1. the containing host directory is mode 0700 and the socket and key are mode
   0600; the CNI refuses a key that is not a regular, non-symlink file owned by
   root and refuses to run the authenticated path as non-root; both peers also
   require Linux `SO_PEERCRED` to report UID 0;
2. a 256-bit random per-agent-start key authenticates every accepted request
   and response with HMAC-SHA-256.

The key is node-local runtime authentication material. It is generated and
atomically replaced by the agent, never stored in a Kubernetes Secret, never
mounted into an application or gateway Pod, and excluded from logs and support
bundles. Agent restart rotates the key. In-flight calls may fail authentication
and retry inside the bounded CNI wait while deny remains installed.
The socket and key must share that one protected directory.

The request authenticator covers the protocol version, a random 128-bit request
ID, RFC3339Nano issue time, HTTP method, exact operation path, and SHA-256 body
digest using length-framed fields. The response authenticator covers version,
the same request ID, status code, and response-body digest. Requests have a
plus-or-minus 30-second freshness window, a bounded 4,096-entry replay cache,
and a one-MiB body limit. Duplicate headers, unknown JSON fields, trailing JSON,
stale or future requests, replay, wrong keys, body/path/status substitution,
and unsigned responses are rejected. Authentication errors are generic and
never echo keys, MACs, Pod metadata, binding data, or endpoints.

The CNI supplies exact Pod namespace, name, UID, sandbox ID, interface, netns
path, and observed netns device/inode. The agent independently confirms Pod UID,
node assignment, enrollment, binding identity, and lifecycle from its cache. It
never treats a name, label, caller-supplied route, or caller-supplied data-plane
configuration as authority. A production prepare operation will name a
controller-authored binding UID/generation; the agent reads the binding itself
and owns programming and live observation. Issue #133 implements that operation
after the resource contract is frozen by #127 and the allocation protocol by
#132.

The agent's Kubernetes credential is projected only into the privileged agent,
with a short lifetime and Kubernetes API audience. Initial Core RBAC is read
only: `get/list/watch` Pods and `VPNWorkloadBinding` objects. Kubernetes RBAC
cannot restrict list/watch to one node, so the informer filters by immutable
node assignment before reconciliation and this cluster-wide metadata visibility
is documented residual risk. The agent receives no Secret, ConfigMap, Namespace,
Gateway, Route, Lease, workload mutation, or status-write permission. A future
observation publication mechanism must not add cluster-wide write authority
without a separate review and abuse tests.

The agent has only the host access required for the supported backend:

- read/write `/run/waycloak` for its socket and ephemeral authentication key;
- read/write `/var/lib/cni/waycloak` for exact owned attachment state;
- read-only access to the supported runtime's netns directory;
- the privilege or exact `NET_ADMIN`/`SYS_ADMIN` capabilities proven necessary
  to enter target netns and manage Waycloak-owned nftables/netlink state.

It has no CNI configuration-directory write mount, container-runtime socket,
host root mount, bpffs/cgroupfs mount for nftables Core, VPN device, gateway
Secret, provider credential, or workload ServiceAccount token. eBPF-specific
host mounts and capabilities require a separate conformance profile and cannot
appear in Core by implication.

## Failure and revocation

Every local-protocol or identity failure makes `ADD`, `CHECK`, or programming
fail closed. Authentication loss is availability loss, never permission to use
ordinary egress. Revoking a route, binding, node capability, agent key, or Pod
UID immediately prevents new programming; existing deny state remains until
exact withdrawal or stale-state GC. Agent crash/restart, node reboot, upgrade,
and key rotation rebuild from durable exact-identity state before readiness.

## Consequences

- An ordinary or compromised non-privileged Pod cannot reach the socket or key,
  forge a request, replay an observed message, or receive node-agent authority.
- Compromising the privileged agent is equivalent to compromising Waycloak
  networking on that node. HMAC does not defend against node root, kernel,
  kubelet, runtime, CNI, or agent compromise; those remain trusted boundaries.
- A stolen old key expires on agent restart, but a root attacker that can read
  the current key is already inside the trusted node boundary.
- Clock correctness within 30 seconds is a support prerequisite. Clock failure
  denies startup rather than weakening authentication.
- Production observation/status publication remains absent from node-agent
  RBAC. ADR 0038 supplies the #133 node-scoped authority model through a
  Pod-bound TokenReview relay owned by the controller.

## Alternatives rejected

- Socket permissions only: no response binding or replay protection.
- A static chart value or Kubernetes Secret: creates long-lived, distributable
  credential material and expands controller/Helm access.
- Bearer tokens in JSON or logs: replayable and easy to disclose.
- TCP on host or Pod networking: expands reachability and certificate lifecycle.
- Trust caller-supplied data-plane configuration: turns the privileged agent
  into a confused deputy for a compromised CNI invocation.
- Give the agent broad write/status RBAC: one compromised node could forge
  readiness or desired state for other nodes.

## Related decisions

- [ADR 0027](0027-explicit-workload-opt-in-and-attachment.md)
- [ADR 0028](0028-reference-authorization-and-cross-namespace-consent.md)
- [ADR 0030](0030-capability-advertisement-and-conformance-profiles.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0038](0038-binding-network-projection-and-node-observations.md)
