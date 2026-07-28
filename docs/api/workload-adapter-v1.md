# Workload adapter protocol v1

Status: Extended, unadvertised pending issue #137 acceptance

The workload-adapter protocol is HTTPS/JSON and is independent of Waycloak Go
packages. Its schema is
[`schemas/workload-adapter-v1.json`](schemas/workload-adapter-v1.json).
Implementations must reject unknown fields, trailing JSON, messages larger than
64 KiB, wrong methods, query strings, stale identities, and unsupported
versions.

The gateway runtime is the lease client and the controller is the health
client. The adapter Service accepts TLS 1.3 mutual authentication from those
two exact configured SPIFFE identities and authorizes them per endpoint: the
controller may call only health, while the gateway runtime may call only lease
delivery and withdrawal. No
Authorization token, Kubernetes credential, VPN credential, discovery, list,
or debug endpoint exists.

## Endpoints

`GET /networking.waycloak.io/adapter/v1/healthz` returns the exact adapter
namespace, name, immutable image digest, adapter Pod UID, observation time, and
live protocol readiness.

`PUT /networking.waycloak.io/adapter/v1/leases/{leaseUID}` applies one complete
lease record. The path UID and body UID must match. The record binds:

- namespace, lease UID, handoff generation, and exact application Pod UID;
- provider public address/port and expiry;
- exact EndpointSlice application Pod address;
- Service backend port and the effective current application target port;
- the complete TCP/UDP protocol set.

Success returns an acknowledgement with the same namespace, lease UID,
generation, Pod UID, and expiry plus a fresh observation time. An
acknowledgement means the adapter has applied and observed the application
state, not merely accepted desired state.

`POST /networking.waycloak.io/adapter/v1/leases/{leaseUID}/withdraw` accepts the
gateway runtime's exact withdrawal identity. Success means current application
delivery for that lease UID, generation, and Pod UID has been removed. The
gateway removes packet rules before requesting this withdrawal and cannot
activate a successor until both are observed absent.

## Generation and restart rules

Generation regression and identity changes within one generation are rejected.
An unchanged current record is idempotent. Expiry-only extension may reobserve
without application churn. An adapter restart must reload durable non-secret
lease state and revalidate the application once before acknowledging. Missing
durable state makes withdrawal unavailable; it must not guess or acknowledge.

## qBittorrent reference profile

The reference adapter declares
`networking.waycloak.io/ProviderAssignedApplicationPort`. It requires both TCP
and UDP, requires `targetPort == publicPort`, uses application-owned HTTPS and
credentials to address the exact EndpointSlice Pod IP, disables random/UPnP
port selection, applies and reads back `listen_port`, probes the TCP listener,
and requests reannounce for all active torrents before acknowledging. On
withdrawal it restores and observes the Service backend port and reannounces.
It receives no Kubernetes or VPN credential and runs in a separate
least-privilege Pod.
