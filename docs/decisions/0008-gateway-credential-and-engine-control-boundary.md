# ADR 0008: Gateway credential and engine-control boundary

Status: Accepted
Date: 2026-07-13

## Context

The gateway manager must observe Gluetun tunnel, DNS, and public-egress health without receiving VPN credentials or exposing Gluetun's broader control API. Gluetun supports credential files and a role-based local HTTP control server. The public `VPNGateway` API references a standard Kubernetes Secret but does not copy its contents.

## Decision

For the initial Proton/OpenVPN contract, the referenced Secret contains `username` and `password` keys. Kubernetes mounts that Secret only into the Gluetun container. Gluetun reads the keys through `OPENVPN_USER_SECRETFILE` and `OPENVPN_PASSWORD_SECRETFILE`; credential values are not placed in the Pod specification or manager environment.

The Gluetun health and control servers bind to Pod loopback. A controller-owned, non-secret ConfigMap grants unauthenticated access only to these loopback GET routes:

- `/v1/dns/status`;
- `/v1/publicip/ip`.

No control-server port is published by the gateway Service. The manager does not receive mutating routes, raw provider responses, the Secret volume, or a Kubernetes API token. The engine adapter converts responses to typed observations and stable errors before they reach status or logs.

## Consequences

- Credential rotation remains a normal Secret update and never copies data into workload namespaces.
- The gateway Pod network namespace is the trust boundary for unauthenticated read-only engine observation.
- A compromised gateway manager can observe the VPN public IP but cannot read VPN credentials through its filesystem mounts or mutate Gluetun through the configured role.
- Supporting WireGuard or another provider may require additional documented Secret keys while preserving the same mount-only boundary.
- NetworkPolicy must not expose Gluetun's loopback control port; the headless Service publishes only VXLAN, DNS, and the Waycloak readiness endpoint.

## Alternatives rejected

- `envFrom` for the entire Secret: exposes unrelated keys and makes the engine environment the credential contract.
- Giving the manager the credential mount: violates least privilege and expands accidental logging risk.
- Disabling Gluetun control-server authentication for every route: unnecessarily exposes mutating and settings endpoints to all gateway containers.
- Giving the manager Kubernetes Secret read permission: creates a broader and less auditable credential path.
