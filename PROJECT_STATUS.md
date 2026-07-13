# Project status

Last updated: 2026-07-13

## Current phase

Waycloak has completed the Phase 1 control-plane exit and the Phase 2 fail-closed data-plane proof. The Go/controller-runtime control plane defines `VPNGateway` and controller-owned `VPNWorkload`, persists stable overlay allocations, quarantines released addresses, performs authorized and idempotent Pod admission, and publishes the UID-bound allocation ConfigMap required by ADR 0005. Admission places the lockdown and verifier init containers before every user init container, including native sidecars.

Phase 1 acceptance passed against the Kubernetes 1.36 local k3s cluster using the same e2e suite that defaults to disposable Kind. It proves unannotated admission is unchanged, unauthorized references are rejected, application startup is blocked while the allocation ConfigMap is absent, allocations survive controller restart and unrelated membership changes, UID binding is preserved, and webhook outage fails closed only for opted-in Pods. Envtest reconciliation also passes against a real API server.

The Linux agent uses native nftables and netlink APIs behind a platform interface. It atomically installs a Pod-UID-owned output-drop chain before protected routing, creates a deterministic VXLAN link, uses protocol-tagged policy rules and a dedicated routing table without replacing the CNI main table, actively verifies an observed gateway overlay health endpoint, and repairs owned link, route, rule, and firewall drift. A two-Pod fake-gateway test on Kubernetes 1.36 k3s proves protected VXLAN reachability, all three cluster-traffic modes, state persistence after agent exit, and no direct fallback after abrupt gateway loss. The standalone lockdown test proves direct packets are dropped and unrelated nftables state is preserved.

Gateway-routed DNS now transparently redirects all UDP and TCP port 53 traffic to the overlay resolver without replacing kubelet search domains. The k3s packet suite proves UDP, TCP fallback, `kubernetes.default` search-domain resolution, DNS survival across owned-state repair and every cluster-traffic mode, and DNS failure without direct fallback after gateway deletion. ADR 0007 records the decision. A daemonless, multi-architecture `ko` build produces the static agent on a digest-pinned distroless nonroot base with an SPDX SBOM; the verified OCI layout contains amd64 and arm64 images.

The exact packaged amd64 image now passes the full admission/allocation lifecycle on the authorized k3s node. The test imports the immutable node-platform manifest, proves both UID-bound init gates complete before the application starts, observes agent readiness only after successful kernel-state repair and gateway health, resolves a Kubernetes search-domain name through the gateway, and proves gateway deletion makes the Pod unready while DNS and ordinary cluster paths remain blocked. It also verifies the application receives neither capabilities nor Kubernetes credentials and that a subsequent unannotated Pod uses ordinary DNS unchanged.

Phase 3 is in progress. `VPNGateway` now reconciles a controller-owned singleton StatefulSet and headless Service when an immutable gateway-manager image is configured. The engine image must also be digest-pinned. The provider Secret is mounted only into the engine container, the gateway manager cannot read it, and the Pod receives no Kubernetes API token. Native owner references provide bounded cleanup, and generated RBAC grants only the StatefulSet, Service, Pod-observation, status, and event operations the controller uses. Unit and real-API envtest coverage prove the resource shape, idempotent ownership, mutable-image rejection, and status regression when the serving Pod disappears.

The gateway-manager runtime and first provider interface now exist. Its Gluetun adapter observes the engine's external tunnel health server, read-only DNS status, and a valid public IP; readiness requires all three and falls immediately when observation fails. Gluetun's control server is bound to loopback and receives a controller-owned role containing only the two required GET routes. ADR 0008 defines the `username`/`password` Secret keys and secret-file boundary. The manager has neither that Secret mount nor a Kubernetes API token, and errors discard provider response bodies. A real k3s test runs the actual manager beside an explicit non-VPN fixture and proves ready, engine-loss unready, and recovery transitions. A daemonless multi-architecture OCI build with SPDX output passes for the gateway-manager binary.

The gated real-provider acceptance now consumes an operator-provisioned credential Secret by reference without reading or printing its values. On Kubernetes 1.36 k3s, the pinned Gluetun engine and actual gateway manager reached composite readiness, the injected protected Pod completed its UID-bound startup gates, and its valid public egress address differed from an ordinary Pod without either address being logged. Both Kubernetes FQDN and search-domain resolution traversed the production split proxy. Abrupt gateway deletion made the protected Pod unready and blocked both direct-IP connectivity and DNS while the ordinary Pod retained egress. The test also proves the application receives no added capabilities, API token, or Secret volume.

Gateway status is now observation-driven at the implemented boundary. The manager readiness probe is composite over tunnel health, a valid VPN public IP, native overlay and forwarding reconciliation, and DNS health; the controller promotes `TunnelReady`, `OverlayReady`, `DNSReady`, and overall `Ready` only from that serving-container signal. Loss of the serving Pod regresses every component and overall readiness. Port-forward-enabled gateways remain not ready because that component is not implemented.

Gateway desired membership is now versioned through the controller-owned ConfigMap without granting the manager API access. The controller joins persisted `VPNWorkload` allocations to UID-matched observed Pod IPs, emits stable member identities plus overlay/underlay addresses in deterministic JSON, and watches both registrations and protected Pod status for incremental updates. The manager validates the complete file before reporting readiness; duplicate identities or addresses fail closed. Adding or removing one member does not derive or rewrite any other allocation.

The gateway manager now reconciles its VXLAN interface and flood-database peers with native netlink operations from that stable desired membership. It installs an owned forward-drop chain before creating VXLAN, then atomically permits only overlay-source traffic from the owned VXLAN interface to the fixed VPN interface, connection-tracked return traffic, and source masquerade on that VPN interface. Gluetun retains its local input/output kill switch; ADR 0009 records the narrowly scoped startup adapter required to delegate forwarding without a direct-egress window.

The production gateway DNS proxy listens on an internal overlay port for both UDP and TCP while the agent transparently redirects application port 53, preserving kubelet search domains. Cluster suffixes go only to the pre-engine observed Kubernetes resolver; all other names go to Gluetun's loopback protected resolver. A pre-engine renderer grants Gluetun's firewall only exact UDP/TCP port-53 access to that observed resolver, and the manager installs an exact destination policy rule and host route around Gluetun's half-default routes. Gluetun's shared-network-namespace input exception hands UDP 4789 to a manager-owned deny-first source allowlist derived from observed members. Agent and gateway readiness probes execute inside their own network namespaces and require composite HTTP health. Fixture and real-provider k3s tests now prove this boundary, including idempotent repair and abrupt gateway loss with no CNI fallback.

Phase 3's functional VPN path is therefore proven. Each controller-owned singleton gateway now also owns a `minAvailable: 1` PodDisruptionBudget, which blocks voluntary eviction without pretending the tunnel is replicated. The deterministic Helm chart installs CRDs, least-privilege RBAC including leader election, two digest-pinned controller/webhook replicas, a zero-unavailable rollout, a controller disruption budget, a webhook Service, and fail-closed admission configurations whose API-server match condition excludes unannotated Pods. It consumes externally managed webhook TLS rather than generating random credentials or requiring cert-manager. Helm lint, repeat rendering, client-side Kubernetes construction, deterministic packaging, multi-architecture controller image construction with SPDX output, and a live k3s install/uninstall acceptance all pass. The live test proves both replicas ready, leader election, disruption policy, unchanged unannotated admission, and clear rejection of an annotated missing-gateway reference.

Install, Pod Security exception, troubleshooting, and safe uninstall guides now document the pre-release source chart and the operational boundaries honestly. Signed OCI publication, provenance, vulnerability policy, compatibility/release manifests, and upgrade/rollback verification remain before `v0.1.0`. The precise next vertical slice is the immutable signed release pipeline tying the three image digests and Helm chart digest to one verified release manifest.

## First deliverable

The first usable release is `v0.1.0`: a single shared Gluetun gateway, injected VXLAN agent, fail-closed egress, standard Kubernetes Secret references, and observable status. Port forwarding follows in `v0.2.0` unless it can be implemented without weakening the first milestone.

## Definition of “implemented”

Do not mark the project implemented because manifests render or Pods become Ready. The first proof requires an end-to-end test demonstrating that:

1. an unannotated Pod uses ordinary cluster egress;
2. an annotated Pod exposes the VPN provider public IP;
3. the annotated Pod loses external connectivity when the VPN tunnel or gateway disappears;
4. DNS cannot bypass the gateway;
5. the workload does not receive VPN credentials;
6. removing the annotation and rolling the workload restores ordinary egress;
7. status identifies which gateway and client allocation the Pod is using.

For port-forward support, qBitTorrent is the mandatory reference workload. TCP and UDP ingress must reach it through the provider lease, and DHT must remain healthy across a sustained crawl and at least one lease renewal.

## Known design risks

- Kubernetes Pod Security `restricted` disallows `NET_ADMIN`; Waycloak needs a tightly scoped policy exception for its injected agent and gateway.
- VXLAN availability and CNI behavior vary by cluster.
- Admission failure policy must preserve fail-closed semantics without blocking unrelated workloads.
- Provider port-forward APIs differ and may only grant one port per tunnel.
- Stable client allocation must not be derived from sorted workload names.
- Shared gateways are a failure domain; horizontal scaling requires deliberate sharding rather than HPA on a singleton tunnel.
