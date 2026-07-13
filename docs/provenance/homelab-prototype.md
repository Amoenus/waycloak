# Homelab prototype provenance

## Why this exists

Waycloak is not a purely speculative design. It generalizes a working shared VPN gateway built in the `Amoenus/homelab` platform. This document preserves the verified behaviors and shortcomings without importing homelab-specific tooling into the product API.

As-built observation date: 2026-07-13.

## Proven architecture

- One Gluetun/OpenVPN gateway served multiple application Pods.
- A VXLAN agent ran inside each protected Pod network namespace.
- Protected applications did not share a Pod with Gluetun.
- qBitTorrent, Bitmagnet, and Loadstone had separate overlay addresses.
- Gateway DNS was reachable on the overlay.
- Proton NAT-PMP acquired multiple forwarded ports on one tunnel.
- Gateway DNAT delivered both TCP and UDP ports to individual client addresses.
- qBitTorrent, Bitmagnet, and Loadstone each consumed forwarded-port information.
- Bitmagnet DHT was observed healthy through the routed gateway.
- Agents monitored gateway reachability and rebuilt VXLAN state after configuration changes.

The homelab developer experience was a KCL `VpnTrait`, compiled by Crossplane/Argo-managed compositions into injected native sidecars, wait initialization, DNS configuration, lease proxying, and a registration resource.

## Problems Waycloak must fix

### Allocation instability

The prototype sorted registration names and assigned addresses by list index. Adding or deleting a member could renumber others. Waycloak requires persisted UID-bound allocations and safe address reuse.

### Disruptive membership reconciliation

The prototype could rebuild shared gateway resources when membership changed, briefly interrupting every protected workload. Waycloak must apply ordinary membership updates incrementally without restarting the tunnel.

### Weak readiness semantics

Prototype registration resources could be `Ready` before actual lease or data-plane delivery was healthy. Waycloak defines layered observed conditions and reserves aggregate readiness for the complete path.

### Hardcoded backend identity

The KCL trait nominally allowed a gateway name while the backend selected a fixed gateway. Waycloak makes named gateway references a real API contract.

### Homelab coupling

The prototype relied on KCL, Crossplane compositions, Argo CD, and ESO. These remain useful homelab consumers but cannot be product runtime dependencies.

### Provider coupling

The working implementation assumed Proton/OpenVPN NAT-PMP behavior. Waycloak places this behind provider capabilities and explicit unsupported states.

### Lifecycle ownership

The prototype included self-reaping behavior and composed-resource ownership. Waycloak assigns lifecycle to a controller with bounded finalizers and Kubernetes-native status.

## Mandatory migration proof

qBitTorrent is the reference production workload. Before the homelab replaces its prototype, Waycloak must prove:

1. VPN egress IP differs from ordinary egress;
2. tunnel/gateway loss is fail-closed;
3. a TCP/UDP provider port reaches qBitTorrent;
4. qBitTorrent is configured with the current public port;
5. DHT is healthy and sustained;
6. lease renewal does not leak or misroute traffic;
7. Bitmagnet and Loadstone can then migrate through the same generic API.

No backward compatibility with the prototype trait implementation is required. The homelab trait should be rewritten as a thin adapter to Waycloak's canonical annotations/API.

## Relevant upstream work

- [`angelnu/pod-gateway`](https://github.com/angelnu/pod-gateway): VXLAN gateway/client data plane and recovery concepts.
- [`angelnu/gateway-admision-controller`](https://github.com/angelnu/gateway-admision-controller): Kubernetes admission-based gateway injection concepts.
- [Gluetun](https://github.com/qdm12/gluetun): initial VPN engine.

Inspect current upstream versions and licenses during implementation rather than relying on values captured in this inception document.
