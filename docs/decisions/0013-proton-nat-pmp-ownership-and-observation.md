# ADR 0013: Proton NAT-PMP ownership and observation

Status: Accepted
Date: 2026-07-13

## Context

Proton's manual OpenVPN port-forwarding contract uses NAT-PMP at
`10.2.0.1:5351`, requests a 60-second mapping, and refreshes it every 45
seconds. The request's internal port is part of the provider mapping identity.
An in-memory or list-index-derived internal port would collide after restart or
renumber another lease when membership changes.

The Kubernetes controller cannot safely operate NAT-PMP because provider
traffic must leave through the VPN tunnel. Conversely, the gateway manager has
no Kubernetes credentials and must not update CR status directly. Gluetun also
has a built-in port-forward loop; running both owners would create competing
mappings.

## Decision

The gateway manager is the sole NAT-PMP owner. The Proton driver uses a native
UDP client, binds its socket to the configured VPN interface on Linux, validates
the connected peer and RFC 6886 response shape, and never returns provider
response bodies in errors. It requests one shared public port for the declared
TCP/UDP protocol set, accepts provider rotation, renews at 75 percent of the
returned lifetime, and sends a zero-lifetime request on release.

The controller persists a unique `providerInternalPort` in each
`PortForwardLease` status. New allocations use the IANA dynamic/private range
49152-65535 so provider mapping identities do not consume privileged or
well-known gateway ports. Existing allocations are never recomputed. The
gateway desired-state ConfigMap publishes the lease object UID, internal port,
previous public-port suggestion, protocols, exact overlay target, and current
public-port generation only after exact target-UID readiness is observed. The manager exposes one current observation at a
versioned, identity-addressed, read-only HTTP endpoint on the serving gateway
Pod; it provides no enumeration endpoint. The controller reads that exact Pod
IP and persists public port and timestamps. `leaseGeneration`
increments only when the public port changes, not on a same-port renewal.

Gluetun receives `PORT_FORWARD_ONLY=on` for compatible server selection and
`VPN_PORT_FORWARDING=off`, leaving acquisition to Waycloak. For Proton OpenVPN,
the referenced credential Secret's username must already include Proton's
`+pmp` suffix. Waycloak does not read, transform, or copy that credential.

A bounded finalizer quarantines a deleted mapping identity for three minutes,
or until a later observed expiry. This covers projected-ConfigMap propagation,
one final 60-second provider lifetime, and release retry without allowing an
external outage to block deletion indefinitely. Test deployments may shorten
the interval explicitly.

Provider acquisition does not imply inbound readiness. `GatewayRulesReady`
requires the separate exact DNAT observation defined by ADR 0014. `Delivered`
and overall `Ready` remain false until workload delivery is observed.

## Consequences

- Controller and gateway-manager restarts retain mapping identity and the last
  public-port suggestion.
- Adding or deleting another lease cannot renumber an existing lease.
- The manager needs no ServiceAccount token or provider credential mount.
- Observations are read from the selected controller-owned gateway Pod IP and
  checked against lease UID, internal port, protocols, and expiry.
- OpenVPN credentials without `+pmp` fail explicitly as provider acquisition
  failure rather than silently enabling a non-VPN path.
- Atomic DNAT and application delivery remain separate required observations;
  ADR 0014 defines the former.

## Alternatives rejected

- Let Gluetun own NAT-PMP and scrape its status file: prevents independent
  stable multi-lease identities and creates an engine-specific core contract.
- Run NAT-PMP in the Kubernetes controller: cannot prove packets traverse the
  selected tunnel and expands provider networking into the control plane.
- Hash the lease UID into a 16-bit port: collision safety would be
  probabilistic.
- Give the gateway manager Kubernetes credentials: unnecessarily expands the
  high-privilege gateway Pod's API authority.
- Reuse an internal port immediately after deletion: can deliver a stale
  provider mapping to a new lease.
