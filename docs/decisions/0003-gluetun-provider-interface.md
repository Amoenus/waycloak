# ADR 0003: Gluetun as the first engine behind a provider interface

Status: Accepted
Date: 2026-07-13

## Context

Gluetun already implements many VPN-provider and protocol combinations and is proven in the originating deployment. Port-forward behavior, however, differs by provider and protocol, and a product API must not equate “VPN” with Proton/OpenVPN forever.

## Decision

Ship Gluetun as the first external VPN engine, pinned by digest and operated by a Waycloak gateway manager. Represent tunnel and port-forward functionality through engine/provider capability interfaces. Implement Proton NAT-PMP as the first multi-lease forwarding driver.

## Consequences

- Initial delivery reuses a mature engine instead of reimplementing VPN clients.
- Gluetun compatibility is part of the release test matrix.
- Provider credentials remain in the gateway.
- Unsupported capabilities are explicit status conditions.
- Other engines/providers can be added without changing workload annotations.

## Alternatives rejected

- Implement OpenVPN/WireGuard clients in Waycloak: unnecessary security and maintenance scope.
- Hardcode Proton semantics in CRDs: prevents a general product.
- Depend solely on Gluetun's single built-in forwarded-port output: insufficient for multiple DHT workloads sharing one tunnel.
