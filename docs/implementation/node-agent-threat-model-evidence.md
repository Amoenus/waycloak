# Node-agent threat model and local-protocol evidence

Date: 2026-07-26

Issue: [#125](https://github.com/Amoenus/waycloak/issues/125)

## Decision

Proceed with the per-node privileged-agent boundary defined by
[ADR 0035](../decisions/0035-node-agent-trust-and-local-protocol.md), subject to
the explicit unsupported categories and residual risks in the
[threat model](../security/threat-model.md).

The production agent is not implemented by this issue. Issue #133 remains
blocked on the replacement resource and UID-binding contracts. This slice
freezes and proves the local authentication envelope without adding a CRD,
status writer, VPN credential path, sidecar, or fallback.

## Vertically tested slice

- Renames the local identity to `networking.waycloak.io/cni-node/v1`.
- Generates and atomically rotates a 256-bit key at each agent start.
- Requires a root-owned mode-0700 directory, mode-0600 regular non-symlink key,
  root CNI execution, and Linux root peer credentials in both directions.
- Authenticates request version, random 128-bit ID, timestamp, method, path and
  body digest, and authenticates response identity, status and body digest.
- Rejects requests outside 30 seconds, replayed IDs, more than 4,096 live IDs,
  duplicate/invalid headers, messages over one MiB, unknown fields, trailing
  JSON, wrong versions, and unauthenticated/tampered responses.
- Rereads the key on each bounded attempt so agent restart/key rotation can
  recover while the already-installed deny remains active.
- Adds privileged STATUS abuse checks for a foreign key and unsafe key mode,
  then proves the current root-only key succeeds.

## Abuse evidence

| Abuse | Evidence |
| --- | --- |
| Wrong key | HMAC unit rejection and privileged STATUS rejection |
| Unsafe directory/key type, owner, mode or non-root process | load-time validation; privileged wrong-mode rejection; Linux unit peer tests |
| Non-root or missing Unix peer identity | Linux handler and `SO_PEERCRED` unit tests |
| Replay | second authenticated request rejected before dispatch |
| Stale request | signed request outside freshness window rejected |
| Method/path/body/status/response tamper | unit MAC-binding tests |
| Oversized input | rejected before operation dispatch |
| Unknown/trailing JSON or protocol version | strict fixture decoder tests |
| Agent restart/key rotation during failed `ADD` | Kind/k3d/homelab focused creation-time proof |
| Authentication outage causing direct egress or application start | positive-control packet capture remains unchanged and sandbox remains failed |

## Least-privilege result

The intended production agent has read-only Pod and `VPNWorkloadBinding`
get/list/watch RBAC, a short-lived projected Kubernetes token, the exact runtime
and state host paths listed in the threat model, and only the privilege proven
necessary for the selected backend. It has no Secret, ConfigMap, Namespace,
Gateway, Route, Lease, mutation or status-write RBAC; no VPN credential; no CNI
configuration write; no runtime socket; and no application mount or token.

Kubernetes cannot field-scope list/watch by node, so cluster-wide Pod/binding
metadata visibility is accepted residual risk for the initial design. The agent
does not receive cross-node status-write authority. Issue #133 must separately
prove a node-scoped observation publication mechanism before any such RBAC is
added.

## Verification record

The authenticated focused proof passes on the authorized k3s/containerd/
Flannel homelab row. Exact PR and CI run links are recorded when the focused
implementation PR reaches its final reviewed commit.
