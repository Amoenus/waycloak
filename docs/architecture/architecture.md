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
