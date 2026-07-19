# Product requirements document

Status: Draft for implementation
Owner: Waycloak maintainers
Last updated: 2026-07-15

## Summary

Waycloak gives Kubernetes workloads declarative, fail-closed egress through shared VPN gateways. A developer marks a Pod template with one named-gateway annotation. Waycloak injects the required networking agent, routes the Pod through that gateway, prevents ordinary-egress fallback, and exposes observed health through Kubernetes status.

Waycloak also coordinates inbound VPN-provider port forwarding for peer-to-peer and server workloads. A single VPN tunnel may provide multiple forwarded ports when the provider supports it; each lease is bound to a stable workload identity and reconciled into gateway DNAT rules.

## Problem

Common Kubernetes VPN patterns force each application to carry a VPN sidecar. This duplicates tunnels and credentials, consumes homelab resources, entangles application manifests with provider details, and makes shared port forwarding difficult. A standalone proxy is insufficient for UDP-heavy protocols such as DHT and does not transparently protect arbitrary Pod traffic.

Existing shared-gateway projects solve portions of routing but generally do not provide the complete product contract required here: explicit workload opt-in, admission-time injection, fail-closed lifecycle, provider-backed multi-workload port leases, application-neutral APIs, and meaningful observed status.

## Users

### Platform operator

Installs Waycloak, defines gateways, supplies engine-native configuration and
credential references, selects optional capability drivers, grants the minimum
necessary security exception, and monitors gateway health.

### Application developer

Selects a gateway on a Pod template. The developer should not need to understand VXLAN, VPN credentials, NAT-PMP, tunnel interfaces, or provider APIs.

### Application integrator

For software that must learn its provider-assigned public port, consumes the Waycloak lease contract through a mounted file, environment-compatible launcher, or local HTTP endpoint.

## Goals

Waycloak is declaratively visible and operationally invisible. A workload opts
in by naming a `VPNGateway` on its Pod template and, when needed, declaring a
`PortForwardLease`. Beyond those explicit Kubernetes intents, existing
application images, protocols, credentials, and configuration should remain
unaware of Waycloak wherever the application semantics permit it. Provider
details and lease churn belong behind the Waycloak boundary.

1. Route opted-in Pod traffic through a named, shared VPN gateway.
2. Fail closed before application startup and throughout tunnel or gateway failure.
3. Preserve normal Kubernetes scheduling; protected applications do not share a Pod with the VPN engine.
4. Keep VPN credentials only on the gateway.
5. Support TCP and UDP, including DHT traffic.
6. Allocate stable client identities and, where supported, durable forwarded-port leases.
7. Make plain Kubernetes annotations and CRDs the universal API.
8. Publish a complete hardened OCI bundle: multi-architecture images, a Helm
   chart containing the served CRDs, and a separately signed optional KCL
   module, all tied together by one signed release manifest.
9. Make routing and lease health visible as conditions, events, logs, and metrics.
10. Support lightweight homelab clusters without requiring a service mesh or replacement CNI.
11. Absorb provider port changes at the gateway so workload listeners stay
    stable, and present the current external port through generic standards
    when an application must advertise it.
12. Consume mature VPN engines through their native configuration surfaces
    rather than duplicating every provider and protocol option in Waycloak.
13. Make unavoidable application adapters independently implementable,
    conformant, least-privilege, and distributable as immutable OCI artifacts.

## Non-goals

- Providing VPN accounts or operating a public VPN service.
- Guaranteeing anonymity against node administrators, privileged workloads, the VPN provider, destination services, or global traffic analysis.
- Protecting a malicious application granted capabilities sufficient to change its own network namespace.
- Replacing Kubernetes NetworkPolicy, the cluster CNI, or a full service mesh.
- Routing host-network Pods in the initial releases.
- Transparent high availability for a single provider tunnel in `v0.1`.
- Supporting Windows nodes.
- Automatically modifying already-running Pod network namespaces after annotations change.
- Configuring every application's proprietary listen-port API in core Waycloak.

## Core user stories

### Select private egress

As an application developer, I annotate a Deployment Pod template with a gateway name. Every replacement Pod uses that gateway or remains unable to reach external networks.

### Remove private egress

As an application developer, I remove the annotation and roll the workload. New Pods use ordinary cluster networking. Existing Pods are not mutated in place.

### Share one tunnel

As a platform operator, I route qBitTorrent, Bitmagnet, and a DHT crawler through one gateway without placing them in one Pod and without copying VPN credentials.

### Receive a forwarded port

As an application integrator, I request a port lease and receive a stable local representation of the current public TCP/UDP port. If the provider changes the port, Waycloak updates the representation and status.

### Observe a failure

As a platform operator, I can distinguish admission failure, route setup failure, gateway reachability, tunnel health, external-IP verification, lease acquisition, and application delivery failures.

## Functional requirements

### FR-1: Gateway declaration and engine configuration

The `VPNGateway` CRD describes a gateway engine, engine-native configuration
references, network pool, DNS mode, optional capability drivers, placement,
resource policy, and allowed workload namespaces. Secrets are referenced, not
copied into the resource, generated configuration, status, or workloads.
Waycloak owns only the documented engine integration settings needed for
observed health, deterministic tunnel identity, firewall handoff, and
single-owner port forwarding. Provider configuration otherwise remains native
to the selected engine.

### FR-2: Workload opt-in

The annotation `networking.waycloak.io/gateway: <namespace>/<name>` on a Pod template is the canonical opt-in. The webhook only mutates Pods that carry it. The referenced gateway's `workloadAccess.namespaceSelector` must authorize the workload namespace. The namespace may be omitted only when the gateway is in the workload namespace.

### FR-3: Admission mutation

Before scheduling, Waycloak injects an initial route setup component and a long-running health/reconnect agent. Mutation is idempotent and versioned. Conflicting manual network configuration is rejected with a useful event.

### FR-4: Fail-closed routing

An injected, required allocation ConfigMap keeps the Pod from starting until the controller has persisted its registration. The init component then installs deny rules before enabling the gateway route or allowing application containers to start. External traffic is permitted only through the Waycloak overlay. Required control traffic is narrowly allowed: gateway overlay, cluster-local traffic according to configured policy, and DNS through the selected resolver. Tunnel loss never restores the node or CNI default route for external traffic.

### FR-5: Shared gateway

One gateway can serve multiple Pods across namespaces subject to policy. Each client receives a stable allocation recorded in Kubernetes state. Adding or removing a client must not renumber existing clients.

### FR-6: DNS containment

Protected Pods resolve external names through the gateway or an explicitly trusted cluster DNS path whose upstream egress is protected. Tests must prove that direct external DNS and fallback resolvers cannot bypass the VPN.

### FR-7: Port-forward leases

The `PortForwardLease` API binds one requested protocol set to one target Pod or Service identity. The provider driver reports its capabilities. Unsupported multi-port or protocol requests fail explicitly. Leases survive controller restarts and are reconciled after gateway replacement.

### FR-8: Lease delivery and adapter boundary

Waycloak provides an application-neutral lease representation containing public port, protocols, gateway, generation, issued time, renewal time, and state. The initial mechanisms are a projected/mounted file and a Pod-local HTTP endpoint. Application-specific adapters remain separate packages or examples.

Gateway translation, standards-based local mapping presentation, and the
neutral contract are always preferred over an application-specific adapter.
An adapter may be added only after acceptance evidence shows that a workload
cannot operate with a stable `spec.target.port`, cannot learn its external
mapping through NAT-PMP/PCP/UPnP, and cannot consume the neutral file or local
HTTP API. It must remain an explicit, least-privilege workload integration
outside the controller, and its design must document why the generic
mechanisms are insufficient. Core conditions must not acquire
application-specific semantics.

Adapters implement a versioned, language-neutral Pod-local protocol and ship
as separate immutable OCI images. Waycloak publishes schemas, fixtures, and a
black-box conformance suite. Adapter selection is explicit and resolves only
operator-trusted digest references; it does not permit arbitrary image
injection. Adapters receive no Kubernetes token, VPN credential, networking
capability, or implicit application credential.

### FR-9: Status

Resources expose `observedGeneration`, conditions with stable reason codes, allocated client address, gateway reference, verified public IP where safe, active lease details, and last transition times. Registration alone is not readiness.

### FR-10: Safe reconciliation

Membership changes should update gateway state without restarting the VPN tunnel. If a disruptive change is unavoidable, status and events must state it and protected workloads must remain fail-closed.

When a serving gateway Pod is replaced and its observed underlay endpoint
changes, Waycloak must propagate the new endpoint to every bound UID-specific
allocation record and the running agents must replace only their owned overlay
peer. The application Pod UID, overlay address, and lease identities remain
stable; application containers are not restarted. Traffic remains fail closed
from loss of the old endpoint until the replacement gateway is observed
healthy, and the transition emits a bounded, useful Kubernetes event.

### FR-11: Uninstall safety

Uninstall documentation must state ordering. The webhook is removed without trapping unrelated Pods, while annotated workloads cannot be recreated unprotected. Finalizers must not indefinitely block namespace deletion.

## Non-functional requirements

### Security

- Least-privilege RBAC and capability assignment.
- No default ServiceAccount token mount where not needed.
- Read-only root filesystems and non-root execution wherever kernel operations allow it.
- Credentials never logged, surfaced in status, or copied to application namespaces.
- Admission and control APIs authenticated and encrypted.
- Explicit network policy examples.

### Reliability

- Controller reconciliation is idempotent.
- Controller restart does not change client addresses or leases.
- Agent reconnect rereads current configuration without requiring application restart where feasible.
- Gateway/tunnel recovery is automatic and observable.

### Performance

- One tunnel must support at least 50 low-throughput protected Pods in a reference Kind/k3d test without per-workload VPN processes.
- The agent target is less than 25 MiB resident memory per protected Pod in `v0.1`; optimize further after measurement.
- Reconciliation must not poll the Kubernetes API per packet or per DNS query.

### Compatibility

- Publish an explicit Kubernetes support matrix.
- Initial target: current Kubernetes minor and the two preceding minors at release time.
- Support common iptables-nft Linux environments first; document legacy iptables limitations.
- Test at least Kind and k3s/k3d.
- Keep data-plane behavior behind a conformance-tested backend interface.
  Optional eBPF support must be explicitly selected, preflighted per node, and
  prove the same packet-level fail-closed behavior before it becomes supported.

## Release acceptance

### v0.1.0 — private egress foundation

- Installable signed Helm OCI chart and signed images.
- One Gluetun-backed gateway.
- Annotation-based injection.
- Stable client allocation.
- TCP/UDP egress and contained DNS.
- Fail-closed startup, tunnel loss, gateway loss, and agent recovery tests.
- Meaningful CRD status and Kubernetes events.
- No dependency on the homelab composition stack.

### v0.2.0 — provider port forwarding

- Capability-aware provider interface.
- Proton/OpenVPN NAT-PMP implementation through Gluetun.
- Multiple stable leases on one tunnel when the provider permits it.
- TCP and UDP DNAT to separate workloads.
- qBitTorrent integration through a separately packaged, least-privilege
  adapter because acceptance evidence shows qBitTorrent 5.2.3 continues to
  announce its local listener after learning a different PCP external port.
- Protocol-faithful acceptance of exact-generation listener rotation and
  tracker advertisement.
- Lease renewal without leaking traffic or silently changing the application contract.
- A signed OCI bundle containing all released images, the CRD-bearing Helm
  chart, and the optional KCL module.
- Replacement of the originating homelab PoC with immutable release artifacts;
  ordinary operation is the release-candidate acceptance environment.

Forced provider rotation, sustained DHT certification, and additional workload
adapters are compatibility expansion rather than blockers for the first
productized port-forward release. They are tracked in `v0.3.0`.

### v0.2.2 — automatic gateway endpoint recovery

- A replacement singleton gateway endpoint is propagated to existing
  UID-bound allocation projections.
- Running agents replace a stale Waycloak-owned VXLAN peer without replacing
  or restarting application containers.
- Overlay addresses and port-forward lease identities remain stable.
- Packaged-image lifecycle acceptance proves fail-closed loss and automatic
  same-Pod recovery against a replacement endpoint.

Acceptance was completed on 2026-07-15 with the signed `v0.2.2` release and a
real Flannel/k3s deployment. Deleting the serving singleton gateway changed its
underlay endpoint. The existing allocation projection and running agent
converged automatically while the application Pod kept its UID, overlay
address, allocation generation, and lease identity. DNS and ordinary egress
failed closed during the transition. The replacement also caused a real Proton
port rotation; the adapter applied matching TCP and UDP listeners before
readiness returned.

### v0.3.0 — provider and workload compatibility

- Sustained Proton renewal and actual port-rotation evidence.
- qBitTorrent ingress, tracker, and DHT certification across renewal or
  rotation without Pod replacement.
- Bitmagnet consumption of the neutral lease contract through an
  evidence-backed narrow adapter. Loadstone remains future compatibility work.
- Broader provider/application troubleshooting evidence from real deployments.
- Engine-native Gluetun configuration with migration from the initial
  provider-shaped convenience fields.
- A public workload-adapter protocol, conformance kit, trusted selection
  mechanism, and qBitTorrent reference adapter.

### v0.4.0 — optional eBPF node-data-plane developer preview

The research selected the prototype-release outcome and E2 architecture: an
optional chained CNI creation-time handoff installs a Pod-parent cgroup eBPF
deny boundary, and a prepared-node agent adopts and reconciles it. The existing
Pod-local nftables/netlink sidecar remains the supported default. Preview
selection is explicit, capability-gated per node, and never falls back.

The release must prove more than attachment. The node path must own the complete
declared feature subset, remove the privileged networking sidecar or demonstrate
another accepted material benefit, pass equivalent fail-closed and lifecycle
tests on amd64 and arm64, and support safe CNI installation and rollback. Initial
compatibility is restricted to the proved k3s/containerd and Flannel integration;
unsupported runtimes, nodes, and feature combinations fail explicitly.

The complete requirements and cutoff are in
[the v0.4.0 release PRD](release-scope-v0.4.md). ADR 0006 remains normative for
the supported production backend; [ADR 0024](../decisions/0024-ebpf-preview-cni-handoff.md)
records the evidence-backed developer-preview direction.

## Success measures

- One-line opt-in is sufficient for application teams.
- Zero observed direct-egress packets from protected Pods during forced failure tests.
- No VPN credential material exists in application Pods.
- Adding a client does not restart the tunnel or renumber existing clients.
- The `v0.2.0` candidate replaces the originating PoC and protects its real
  workload without mutable or unrecorded artifacts.
- By `v0.3.0`, qBitTorrent DHT remains healthy through a sustained test and
  lease renewal or provider rotation.
- Fresh users can install and validate protected egress using only Kubernetes, Helm, and a compatible VPN account.

## Resolved design questions

- The initial agent uses native nftables and netlink exclusively and rejects unsupported kernels; it has no permissive iptables fallback ([ADR 0006](../decisions/0006-native-linux-data-plane.md)).
- The chart consumes an externally managed webhook TLS Secret and CA bundle. Optional certificate automation may produce those inputs but is not a runtime dependency ([ADR 0010](../decisions/0010-external-webhook-certificate-ownership.md)).
- Renewable leases use the mounted file and Pod-loopback endpoint as canonical live state. Environment-only applications explicitly use a fail-closed supervisor that restarts its child on generation changes; the controller does not restart arbitrary workload owners ([ADR 0011](../decisions/0011-renewable-port-lease-delivery.md)).
- Stable target-port DNAT preserves the local listener while a standards-based
  Pod-local mapping surface exposes the actual external port to applications
  that must advertise it. Workload-specific adapters are last-resort, opt-in
  integrations and never controller behavior
  ([ADR 0015](../decisions/0015-stable-target-port-translation.md)).
- VPN engines consume operator-owned native configuration while Waycloak
  reserves only its health, interface, firewall, and ownership boundary
  ([ADR 0017](../decisions/0017-engine-native-configuration-boundary.md)).
- Workload-specific integrations use a versioned out-of-process adapter
  protocol and trusted immutable OCI artifacts
  ([ADR 0018](../decisions/0018-workload-adapter-protocol.md)).
- eBPF remains a conformance-gated optional backend; nftables/netlink
  remains the supported production backend until measured evidence justifies
  an accepted follow-up decision
  ([ADR 0019](../decisions/0019-optional-ebpf-data-plane.md)).
- The first eBPF implementation is a developer-preview CNI creation-time
  handoff plus node owner; it is not the default or a production support claim
  ([ADR 0024](../decisions/0024-ebpf-preview-cni-handoff.md)).
