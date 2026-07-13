# ADR 0001: Shared gateway over per-workload VPN sidecars

Status: Accepted
Date: 2026-07-13

## Context

Per-workload VPN sidecars provide strong network-namespace sharing but duplicate tunnels, credentials, control APIs, and resource consumption. Combining all protected applications into one Pod avoids tunnel duplication but creates an unacceptable scheduling, lifecycle, and failure boundary.

A homelab prototype proved that separate application Pods can share one Gluetun tunnel over VXLAN while supporting UDP/DHT and provider port forwarding.

## Decision

Waycloak uses shared gateway Pods and injects a small routing agent into each opted-in workload Pod. Applications remain independently scheduled and managed.

## Consequences

- The gateway becomes an explicit shared failure domain.
- Agent and gateway need narrowly scoped `NET_ADMIN`.
- VXLAN and CNI compatibility become product prerequisites.
- Credentials and heavy VPN processes are centralized.
- Scaling uses explicit gateways/shards, not a blind HPA.

## Alternatives rejected

- VPN sidecar per application: excessive tunnel and credential overhead.
- All applications in gateway Pod: unacceptable coupling.
- HTTP/SOCKS proxy only: does not transparently cover arbitrary TCP/UDP and DHT.
- Replacement CNI: too large a dependency and operational scope for the initial product.
