# ADR 0038: Credential-free binding projection and node-scoped observations

Status: Accepted
Date: 2026-07-26

## Context

ADR 0035 forbids the privileged node agent from trusting CNI-supplied
data-plane configuration and grants it read access only to Pods and
`VPNWorkloadBinding` objects. The frozen binding from ADR 0036 contained an
allocated address but omitted the gateway endpoint, overlay identity, health
port, MTU and cluster-traffic policy required by the native backend. Reading
Gateways or a hidden ConfigMap would widen authority and recreate an implicit
runtime contract.

The binding status contract also requires fresh agent observations, while
granting one DaemonSet service account status write across every namespace
would let a compromised node forge readiness for other nodes.

## Decision

The binding controller copies the minimum credential-free desired network
state into `VPNWorkloadBinding.spec.network`: current gateway generation,
overlay CIDR and gateway address, observed underlay endpoint, overlay health
port, VNI, MTU and the reviewed cluster-traffic policy. The source is the
current, Ready `VPNGateway` generation and its typed observed addresses. The
projection contains no Secret, provider account data, VPN credential,
Kubernetes credential or workload credential. The node agent continues to
read only Pods and bindings.

The CNI sends only the exact binding UID and generation in `prepare` and
`check`. The agent independently re-reads the Pod and binding, confirms Pod
UID, node, binding UID, gateway UID and generation, derives its own native
configuration, programs with lockdown retained and verifies the live gateway
path. Configuration and verification failure restore lockdown before the
operation fails.

The agent publishes bounded observations to an HTTPS controller endpoint with
a short-lived, Kubernetes-audience, Pod-bound service-account token. The
controller authenticates the token with `TokenReview`, resolves the bound agent
Pod UID and scheduled node, requires the configured installation namespace and
node-agent ServiceAccount on both the token and live Pod, and accepts
observations only for current bindings
whose immutable `nodeName` equals that node. The controller, not the agent,
writes binding status. The receive time is authoritative. A non-ready
observation may confirm withdrawal for a deleting binding; a ready observation
may not.

The local protocol remains the only CNI programming interface. The remote
endpoint accepts observations only and cannot program a node. TLS key and CA
lifecycle are installation inputs and may not silently disable certificate or
token validation.

## Consequences

- ADR 0036 is amended by the required `spec.network` object and its scalar
  validation. This is pre-release API correction, not compatibility support.
- Workload owners with binding read permission can see credential-free network
  coordinates already required for diagnostics; support bundles still redact
  endpoints by default.
- A compromised node-agent Pod can forge health only for bindings assigned to
  its own node. It cannot write Kubernetes status or assert another node.
- Controller or relay loss makes observations stale and `Ready` non-true;
  packet enforcement remains node-local and fail closed.
- Agent restart rebuilds from exact durable CNI records before reporting
  readiness. Missing authority locks down; only an absent exact Pod permits
  cleanup.

## Alternatives rejected

- Trust a CNI-supplied configuration: creates a privileged confused deputy.
- Let the agent read Gateway, ConfigMap, Lease or Secret objects: widens
  metadata and credential authority beyond the reviewed node contract.
- Grant the shared node-agent service account status patch: permits cross-node
  readiness forgery.
- Publish observations through annotations or ConfigMaps: creates a hidden API
  and weakens field ownership.
- Treat controller/relay loss as permission to retain an allow path: violates
  fail-closed readiness and revocation semantics.

## Related decisions

- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0035](0035-node-agent-trust-and-local-protocol.md)
- [ADR 0036](0036-replacement-api-freeze.md)
- [ADR 0037](0037-uid-bound-allocation-and-quarantine.md)
