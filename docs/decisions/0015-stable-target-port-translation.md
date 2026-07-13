# ADR 0015: Stable target ports and generic external-port presentation

Status: Accepted
Date: 2026-07-13

## Context

Provider-assigned public ports can rotate at renewal. The homelab proof of
concept colocated Gluetun and qBitTorrent and used qSticky plus a local
compatibility proxy to copy each new public port into qBitTorrent. That shape
made the application's listening configuration part of provider-lease
reconciliation.

Waycloak has a separate shared gateway and an explicit `PortForwardLease`
target. Its provider mapping already owns a stable internal port, and its
gateway data plane can translate that port to a fixed workload port.

That translation is sufficient for an ordinary TCP or UDP listener, but it is
not sufficient for every application protocol. BitTorrent clients advertise a
peer port through trackers and DHT. If qBitTorrent advertises its stable local
port while Proton assigned a different public port, inbound packets can reach
the DNAT rule but peers were told to use the wrong public endpoint. Rewriting
only packet headers cannot repair an application-layer advertisement.

A Kubernetes compatibility probe with qBitTorrent 5.2.3 confirmed the
boundary. qBitTorrent accepted PCP TCP and UDP mappings from local port `6881`
to external port `42000` and logged both as successful. Its subsequent real
HTTP tracker announce nevertheless sent `port=6881`. Standards-based mapping
presentation therefore does not make this qBitTorrent version operationally
unaware of a different provider public port.

## Decision

Applications listen on the fixed `spec.target.port` declared by their
`PortForwardLease`. The gateway atomically translates the provider mapping to
the exact UID-bound Pod overlay address and that fixed target port. A public
port rotation increments the lease generation and replaces gateway rules, but
does not change the Pod or the target listener.

When a workload must advertise its externally reachable port, Waycloak first
presents the current UID- and generation-bound mapping through an
application-neutral standard such as NAT-PMP, PCP, or UPnP. The presentation
endpoint is local to the protected Pod, grants only the lease already bound to
that Pod, and cannot request arbitrary mappings. The neutral renewable record
remains the canonical state and is also available to consumers that can use
the versioned file or loopback API directly.

This is the general product order of preference:

1. translate provider state at the gateway while the workload keeps a stable
   local listener;
2. present the current external mapping through a standard router protocol
   when the workload must advertise it;
3. expose provider-neutral state through the versioned file or loopback API;
4. add a workload-specific adapter only if none of the generic mechanisms can
   meet the application's semantics.

Waycloak is declaratively visible and operationally invisible: the Pod names a
gateway and lease in Kubernetes, while provider and networking mechanics stay
outside the application wherever possible. The qBitTorrent compatibility
evidence satisfies the exception threshold. Its integration uses a narrow,
separately packaged sidecar that reads the neutral record and updates
qBitTorrent; no qBitTorrent behavior enters the controller or gateway manager.

Any workload-specific adapter requires acceptance evidence that the standard
presentation and neutral API are insufficient, plus an explicit design
justification. It must be opt-in, separately packaged from the controller,
least privilege, and must not give the application Kubernetes or VPN
credentials. Where it affects readiness, acknowledgement must bind the exact
lease identity and generation.

For ordinary fixed-port workloads, `Delivered` continues to describe the
neutral record and observed Waycloak data plane. An adapter-managed integration
must additionally acknowledge the exact lease identity, generation, and
applied application port before `Delivered=True`. End-to-end acceptance still
proves that the application is actually listening and reachable; status does
not infer that from desired configuration.

## Consequences

- Provider rotation remains operationally invisible to ordinary fixed-port
  listeners.
- Applications that honor a standard mapping protocol can learn the current
  mapping without a provider-specific or application-specific API.
- qSticky and qBitTorrent WebUI credentials are not part of Waycloak's core
  runtime or security boundary.
- Future workload-specific adapters have a documented high bar and cannot
  silently become core control-plane behavior.
- One workload manifest works across providers whose public-port behavior
  differs.
- The target port remains stable across controller, gateway, and workload
  restarts.
- Applications that truly require the public port can still consume the
  versioned neutral file or loopback API under ADR 0011.
- qBitTorrent requires an application adapter, but that exception does not
  weaken the default workload-agnostic architecture.

## Alternatives rejected

- Claim gateway DNAT alone is transparent to qBitTorrent: it leaves tracker
  and DHT advertisements pointing at the stable local port when the provider
  public port differs.
- Emulate Gluetun's control API in the workload agent: this leaks a
  provider-engine compatibility contract into the canonical workload path.
- Embed qBitTorrent WebUI calls in the controller: this makes core readiness
  application-specific and requires application credentials in the control
  plane.
- Rewrite BitTorrent tracker or DHT payloads in the gateway: this is a fragile,
  application-specific middlebox and cannot cover encrypted or future protocol
  variants safely.
- Inject the public port as an environment variable: it becomes stale after
  renewal and cannot update a running process.
