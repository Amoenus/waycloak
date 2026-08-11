# CNI to node-agent protocol

Status: Baseline security contract; production programming operation reserved for
issue #133

Protocol identity: `networking.waycloak.io/cni-node/v1`

Transport: HTTP semantics over one root-owned Unix socket. There is no TCP
listener and no Kubernetes Service.

## Authentication envelope

Every request includes exactly one of each header:

| Header | Value |
| --- | --- |
| `X-Waycloak-Protocol` | exact protocol identity |
| `X-Waycloak-Request-Id` | base64url 128-bit random value |
| `X-Waycloak-Issued-At` | UTC RFC3339Nano timestamp |
| `X-Waycloak-Authentication` | base64url HMAC-SHA-256 |

The request MAC length-frames and authenticates `request`, version, request ID,
issue time, method, path, and the base64url SHA-256 body digest. The response
echoes version and request ID and authenticates `response`, version, request ID,
decimal status, and response-body digest. Accepted error responses are signed;
requests that fail authentication receive only a generic unsigned denial.

The key is 32 random bytes, replaced atomically at agent start. Directory,
socket, and key permissions are 0700/0600/0600. The CNI reads the key for each
call so a bounded retry can recover after rotation. It rejects symlinks,
non-regular files, non-root ownership, non-root execution, wrong permissions,
and wrong length. The server rejects a Unix peer whose Linux `SO_PEERCRED` UID
is not 0, and the CNI rejects a server peer whose UID is not 0. Peer credentials
are defense in depth; HMAC still binds the message and response.
The socket and key must be absolute paths in the same protected directory.

Requests outside the plus-or-minus 30-second window, duplicate request IDs,
more than 4,096 unexpired IDs, bodies over one MiB, duplicate authentication
headers, or invalid MACs are rejected. Replay entries are process-local because
agent restart also rotates the key.

## Common identity schema

Pod-scoped requests carry:

```json
{
  "apiVersion": "networking.waycloak.io/cni-node/v1",
  "pod": {
    "namespace": "apps",
    "name": "client",
    "uid": "exact-pod-uid",
    "containerID": "exact-sandbox-id",
    "ifName": "eth0",
    "netNS": "/var/run/netns/exact-path"
  }
}
```

The CNI durable record also binds the netns device/inode. The production agent
must independently confirm UID, `spec.nodeName`, sandbox/netns identity,
enrollment, and binding UID/generation before privilege is exercised. Unknown
fields and trailing values are invalid. Messages never contain a Secret value,
VPN credential, Kubernetes bearer token, provider account identifier, or
unredacted private endpoint.

## Operations

| Operation | Method/path | Authority and result |
| --- | --- | --- |
| Status | `GET /cni-node/v1/status` | authenticated agent/version/capability liveness; never data-plane readiness by itself |
| Resolve | `POST /cni-node/v1/resolve` | confirms exact Pod UID/node and whether the Pod was explicitly enrolled |
| Binding | `POST /cni-node/v1/binding` | feasibility subset: confirms exact UID-bound desired state; #132 replaces fixture allocation fields |
| Prepare | `POST /cni-node/v1/prepare` | reserved for #133: agent independently reads the named binding, adopts locked state, programs, observes, and returns an observed generation |
| Check | `POST /cni-node/v1/check` | confirms exact current ownership plus live programmed/observed generation |
| Withdraw | `POST /cni-node/v1/withdraw` | withdraws only exact UID/sandbox/netns-owned state; idempotent missing state succeeds |

The baseline does not add generic command execution, arbitrary nftables/netlink input,
filesystem paths outside the exact attachment, raw Kubernetes objects, log
retrieval, Secret retrieval, gateway control, or provider operations. Adding an
operation or field requires a protocol-version compatibility decision, threat
review, request-size bound, owner/revocation definition, and abuse tests.

## Error contract

Authentication denials are generic and unsigned. Authenticated operation errors
use a strict response containing `apiVersion` and `error.code`, `retryable`, and
a concise non-sensitive `message`. Initial stable codes are `InvalidRequest`,
`PodIdentityMismatch`, `NotEnrolled`, and `BindingNotReady`; #132 and #133 add
binding/programming reasons only with protocol review. HTTP messages are not
stable API. CNI treats unavailable, unauthorized,
stale, replayed, unsupported, unresolved, rejected, unprogrammed, or
unverifiable responses as failure. No response requests or permits fallback.

## Rotation and recovery

Agent startup writes a new key atomically before serving a replacement socket.
An in-flight call using the old key fails and retries only inside its existing
bounded operation deadline. Deny-first state remains installed. Durable state
is keyed by exact Pod/sandbox/netns identity, so restart recovery cannot adopt a
same-name or reused namespace accidentally. Old keys and request IDs are not
accepted after restart.
