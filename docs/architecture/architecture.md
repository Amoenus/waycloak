# Replacement architecture

Waycloak separates declarative intent from the privileged packet boundary:

- `VPNGatewayClass` declares distribution-owned controller, release, feature, and conformance identity.
- `VPNGateway` declares operator-owned tunnel intent and Secret/native configuration references.
- `VPNEgressRoute` is the workload-owner attachment API.
- one same-namespace Pod-template label enrolls a workload.
- a controller-authored `VPNWorkloadBinding` binds allocation intent to the exact Pod UID.
- the chained CNI installs deny-first state before sandbox creation succeeds.
- the privileged node agent programs, observes, repairs, and withdraws node networking through an authenticated local protocol.

Application containers are not mutated. Admission rejects old annotations and defends static ownership rules, but admission is not the packet-security boundary. See [Kubernetes API maturity](kubernetes-api-maturity.md) for the complete ownership and lifecycle model.

## Extension boundaries

Waycloak integrates VPN behavior through the Gluetun engine adapter. That
adapter owns Gluetun-native configuration, tunnel and DNS observation, public
egress observation, firewall cooperation, and discovery of optional engine
capabilities. Waycloak does not duplicate Gluetun's provider catalogue.

Optional provider mechanisms sit behind an engine-capability interface. The
first implementation is Proton NAT-PMP port acquisition for a Gluetun
`protonvpn`/`openvpn` tunnel. It owns only mapping acquisition, renewal,
observation, and release; it is not a Proton VPN adapter. The generic Waycloak
runtime continues to own lease identity, stable application ports, Service/Pod
selection, translation, gateway rules, handoff, status, and fail-closed
withdrawal.

Application-specific `WorkloadAdapter` implementations are the final
compatibility layer. They are used only when fixed-port translation, a standard
application protocol, or the neutral lease record cannot make the application
consume or advertise the active mapping. They run out of process and receive no
Kubernetes token, VPN credential, Linux capability, or ownership of gateway
packet policy. See [ADR 0043](../decisions/0043-engine-capabilities-and-application-adapters.md).
