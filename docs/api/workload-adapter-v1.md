# Workload adapter protocol v1

Status: Stable optional capability

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

The wire operation has three internal responsibilities. `Apply` mutates the
application only for a changed lease/target/Pod/generation/public-port
identity. `Observe` independently verifies the exact application state and all
required listener protocols with retry bounded below the fail-closed freshness
deadline. `RenewAcknowledgement` advances an expiry-only record from a durable
successful identity without logging in, rewriting preferences, reannouncing,
or changing handoff generation. This separation is an implementation rule and
does not add a language-specific endpoint or change the JSON schema.

HTTP `409 Conflict` is reserved for permanent stale or contradictory identity,
generation, or port input. Dial, authentication, application API, and listener
observation failures return `503 Service Unavailable`; clients preserve that
classification through the gateway-runtime boundary.

`POST /networking.waycloak.io/adapter/v1/leases/{leaseUID}/withdraw` accepts the
gateway runtime's exact withdrawal identity. Success means current application
delivery for that lease UID, generation, and Pod UID has been removed. The
gateway removes packet rules before requesting this withdrawal and cannot
activate a successor until both are observed absent.

## Generation and restart rules

Generation regression and identity changes within one generation are rejected.
An unchanged current record is idempotent. Expiry-only extension uses renewal
acknowledgement without an application call. An adapter restart must reload
durable non-secret lease state and reobserve, but not reapply, the application
once before acknowledging. Missing durable state makes withdrawal unavailable;
it must not guess or acknowledge.

The controller persists a new `Selecting` endpoint and handoff generation
before the gateway runtime may acquire, program, or deliver it. A status-write
retry therefore reuses the same exact generation; it cannot leave an adapter
one generation ahead of the durable lease and then issue a stale withdrawal.

## qBittorrent reference profile

The reference adapter declares
`networking.waycloak.io/ProviderAssignedApplicationPort`. It requires both TCP
and UDP, requires `targetPort == publicPort`, uses application-owned HTTPS and
credentials to address the exact EndpointSlice Pod IP, disables random/UPnP
port selection, applies and reads back `listen_port`, probes the TCP listener,
proves the UDP listener with a transaction-bound BEP 5 DHT ping, and requests
reannounce for all active torrents before acknowledging. On
withdrawal it restores and observes the Service backend port and reannounces.
It receives no Kubernetes or VPN credential and runs in a separate
least-privilege Pod.
