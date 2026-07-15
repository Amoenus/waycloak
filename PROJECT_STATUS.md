# Project status

Last updated: 2026-07-14

## Current phase

Waycloak has completed the Phase 1 control-plane exit and the Phase 2 fail-closed data-plane proof. The Go/controller-runtime control plane defines `VPNGateway` and controller-owned `VPNWorkload`, persists stable overlay allocations, quarantines released addresses, performs authorized and idempotent Pod admission, and publishes the UID-bound allocation ConfigMap required by ADR 0005. Admission places the lockdown and verifier init containers before every user init container, including native sidecars.

Phase 1 acceptance passed against the Kubernetes 1.36 local k3s cluster using the same e2e suite that defaults to disposable Kind. It proves unannotated admission is unchanged, unauthorized references are rejected, application startup is blocked while the allocation ConfigMap is absent, allocations survive controller restart and unrelated membership changes, UID binding is preserved, and webhook outage fails closed only for opted-in Pods. Envtest reconciliation also passes against a real API server.

The Linux agent uses native nftables and netlink APIs behind a platform interface. It atomically installs a Pod-UID-owned output-drop chain before protected routing, creates a deterministic VXLAN link, uses protocol-tagged policy rules and a dedicated routing table without replacing the CNI main table, actively verifies an observed gateway overlay health endpoint, and repairs owned link, route, rule, and firewall drift. A two-Pod fake-gateway test on Kubernetes 1.36 k3s proves protected VXLAN reachability, all three cluster-traffic modes, state persistence after agent exit, and no direct fallback after abrupt gateway loss. The standalone lockdown test proves direct packets are dropped and unrelated nftables state is preserved.

Gateway-routed DNS now transparently redirects all UDP and TCP port 53 traffic to the overlay resolver without replacing kubelet search domains. The k3s packet suite proves UDP, TCP fallback, `kubernetes.default` search-domain resolution, DNS survival across owned-state repair and every cluster-traffic mode, and DNS failure without direct fallback after gateway deletion. ADR 0007 records the decision. A daemonless, multi-architecture `ko` build produces the static agent on a digest-pinned distroless nonroot base with an SPDX SBOM; the verified OCI layout contains amd64 and arm64 images.

The exact packaged amd64 image now passes the full admission/allocation lifecycle on the authorized k3s node. The test imports the immutable node-platform manifest, proves both UID-bound init gates complete before the application starts, observes agent readiness only after successful kernel-state repair and gateway health, resolves a Kubernetes search-domain name through the gateway, and proves gateway deletion makes the Pod unready while DNS and ordinary cluster paths remain blocked. It also verifies the application receives neither capabilities nor Kubernetes credentials and that a subsequent unannotated Pod uses ordinary DNS unchanged.

Phase 3 is complete. `VPNGateway` reconciles a controller-owned singleton StatefulSet and headless Service when an immutable gateway-manager image is configured. The engine image must also be digest-pinned. The provider Secret is mounted only into the engine container, the gateway manager cannot read it, and the Pod receives no Kubernetes API token. Native owner references provide bounded cleanup, and generated RBAC grants only the StatefulSet, Service, Pod-observation, status, and event operations the controller uses. Unit and real-API envtest coverage prove the resource shape, idempotent ownership, mutable-image rejection, and status regression when the serving Pod disappears.

The initial API-question backlog is closed normatively. ADR 0006 fixes the native nftables/netlink backend without permissive iptables fallback; ADR 0010 fixes externally owned webhook TLS without a cert-manager dependency; and ADR 0011 fixes renewable lease delivery as an atomic file plus Pod-loopback record, with an explicit fail-closed supervisor for environment-only applications rather than controller-driven workload restarts. Phase 4 still owns the concrete `PortForwardLease` container-selection and adapter-packaging fields.

The gateway-manager runtime and first provider interface now exist. Its Gluetun adapter observes the engine's external tunnel health server, read-only DNS status, and a valid public IP; readiness requires all three and falls immediately when observation fails. Gluetun's control server is bound to loopback and receives a controller-owned role containing only the two required GET routes. ADR 0008 defines the `username`/`password` Secret keys and secret-file boundary. The manager has neither that Secret mount nor a Kubernetes API token, and errors discard provider response bodies. A real k3s test runs the actual manager beside an explicit non-VPN fixture and proves ready, engine-loss unready, and recovery transitions. A daemonless multi-architecture OCI build with SPDX output passes for the gateway-manager binary.

The gated real-provider acceptance now consumes an operator-provisioned credential Secret by reference without reading or printing its values. On Kubernetes 1.36 k3s, the pinned Gluetun engine and actual gateway manager reached composite readiness, the injected protected Pod completed its UID-bound startup gates, and its valid public egress address differed from an ordinary Pod without either address being logged. Both Kubernetes FQDN and search-domain resolution traversed the production split proxy. Abrupt gateway deletion made the protected Pod unready and blocked both direct-IP connectivity and DNS while the ordinary Pod retained egress. The test also proves the application receives no added capabilities, API token, or Secret volume.

Gateway status is now observation-driven at the implemented boundary. The manager readiness probe is composite over tunnel health, a valid VPN public IP, native overlay and forwarding reconciliation, and DNS health; the controller promotes `TunnelReady`, `OverlayReady`, `DNSReady`, and overall `Ready` only from that serving-container signal. Loss of the serving Pod regresses every component and overall readiness. Port-forward-enabled gateways remain not ready because that component is not implemented.

Gateway desired membership is now versioned through the controller-owned ConfigMap without granting the manager API access. The controller joins persisted `VPNWorkload` allocations to UID-matched observed Pod IPs, emits stable member identities plus overlay/underlay addresses in deterministic JSON, and watches both registrations and protected Pod status for incremental updates. The manager validates the complete file before reporting readiness; duplicate identities or addresses fail closed. Adding or removing one member does not derive or rewrite any other allocation.

The gateway manager now reconciles its VXLAN interface and flood-database peers with native netlink operations from that stable desired membership. It installs an owned forward-drop chain before creating VXLAN, then atomically permits only overlay-source traffic from the owned VXLAN interface to the fixed VPN interface, connection-tracked return traffic, and source masquerade on that VPN interface. Gluetun retains its local input/output kill switch; ADR 0009 records the narrowly scoped startup adapter required to delegate forwarding without a direct-egress window.

The production gateway DNS proxy listens on an internal overlay port for both UDP and TCP while the agent transparently redirects application port 53, preserving kubelet search domains. Cluster suffixes go only to the pre-engine observed Kubernetes resolver; all other names go to Gluetun's loopback protected resolver. A pre-engine renderer grants Gluetun's firewall only exact UDP/TCP port-53 access to that observed resolver, and the manager installs an exact destination policy rule and host route around Gluetun's half-default routes. Gluetun's shared-network-namespace input exception hands UDP 4789 to a manager-owned deny-first source allowlist derived from observed members. Agent and gateway readiness probes execute inside their own network namespaces and require composite HTTP health. Fixture and real-provider k3s tests now prove this boundary, including idempotent repair and abrupt gateway loss with no CNI fallback.

Phase 3's functional VPN path is therefore proven. Each controller-owned singleton gateway now also owns a `minAvailable: 1` PodDisruptionBudget, which blocks voluntary eviction without pretending the tunnel is replicated. The deterministic Helm chart installs CRDs, least-privilege RBAC including leader election, two digest-pinned controller/webhook replicas, a zero-unavailable rollout, a controller disruption budget, a webhook Service, and fail-closed admission configurations whose API-server match condition excludes unannotated Pods. It consumes externally managed webhook TLS rather than generating random credentials or requiring cert-manager. Helm lint, repeat rendering, client-side Kubernetes construction, deterministic packaging, multi-architecture controller image construction with SPDX output, and a live k3s install/uninstall acceptance all pass. The live test proves both replicas ready, leader election, disruption policy, unchanged unannotated admission, and clear rejection of an annotated missing-gateway reference.

Install, Pod Security exception, troubleshooting, and safe uninstall guides document the signed release and operational boundaries. The protected-tag workflow uses full-SHA Actions, checksum-verified Trivy and Gitleaks binaries, multi-architecture `ko` publication, HIGH/CRITICAL vulnerability gates, keyless Cosign signatures, SPDX attestations, GitHub build provenance, deterministic chart digest preparation, and pre-release signature verification. [Workflow run 29269658337](https://github.com/Amoenus/waycloak/actions/runs/29269658337) published [Waycloak v0.1.0](https://github.com/Amoenus/waycloak/releases/tag/v0.1.0) from commit `c82ec4f57fd845a6715b365686491de1423a5209`. Its signed manifest ties all four OCI artifact digests, compatibility evidence, required capabilities, and the pinned Gluetun identity together.

The verified v0.1 history is now contained in `main` at `f8e35de`; the `v0.1.0` tag remains on its original source commit so existing signatures, attestations, and the release manifest stay coherent. Future tag workflows explicitly fetch `origin/main` and refuse publication unless the tagged commit is already in that branch. CI Helm validation no longer invokes Kubernetes discovery from the clusterless verification job; live construction remains covered by the dedicated Kind acceptance job and release Kind gate.

Independent post-release verification matched all seven release-asset hashes to GitHub metadata; verified the exact tag-workflow Cosign identity for the three images, chart, and release manifest; verified GitHub provenance for every OCI artifact and release file; confirmed Linux amd64/arm64 indexes and tag-to-manifest digest identity for every image; and pulled, linted, and rendered the OCI chart at digest `sha256:923a61a224b2da61005cc408dfff7e5a41dba3f9ace48dcf1f96cc4e0539b148`. The Phase 3 exit is satisfied.

Helm lifecycle acceptance proves a zero-unavailable controller upgrade, rollback to the recorded revision, two-phase webhook CA rotation, serving-certificate replacement, and fail-closed admission after the old CA is removed. Gateway StatefulSets use `OnDelete`; template reconciliation emits `GatewayRolloutRequired` instead of automatically destroying the singleton tunnel, and the operator guide requires one-at-a-time activation during an explicit fail-closed window. The next roadmap work is Phase 4's provider-capability interface and `PortForwardLease` API vertical slice.

Phase 4 began with its API and observed target-binding boundary. The generated `PortForwardLease` CRD accepts a non-empty Pod selector, local port, TCP/UDP protocol set, and authorized gateway reference. Its controller requires exactly one eligible Pod protected by that gateway and binds status to the exact Pod UID plus persisted `VPNWorkload` overlay allocation. `Fixed` targets require whole-Pod readiness; `ProviderAssigned` targets require a Running Pod with the injected Waycloak agent Ready so adapter readiness can wait for the first delivered lease without a bootstrap cycle. ADR 0012 records the object-UID lease identity and deliberately deferred Service handoff. A provider-neutral interface exposes observed protocols, capacity, shared-port/requested-port behavior, minimum duration, idempotent ensure, and release operations. Unit and real-API envtest coverage prove authorization, ambiguity rejection, schema rejection of empty selectors, UID-bound target observation, idempotence, and target regression after Pod deletion.

The Proton/OpenVPN NAT-PMP slice is now implemented behind that interface. The Linux driver binds UDP to the selected tunnel, validates RFC 6886 responses, acquires a shared TCP/UDP port, accepts rotation, renews the returned 60-second lease at 45 seconds, and releases with a zero lifetime. Kubernetes persists a collision-free provider internal port and public-port generation; adding another lease cannot renumber it, and a bounded finalizer quarantines deleted identities across ConfigMap projection and provider expiry. The tokenless gateway manager owns acquisition and publishes a read-only observation, while the controller reads the exact serving Pod. Gluetun's competing lease loop stays disabled. Protocol-faithful live-cluster tests prove acquisition and port rotation without claiming a real provider. ADR 0013 records the boundary.

Atomic gateway DNAT is now implemented with native nftables. The controller publishes a lease only while its UID-bound overlay target is also an observed gateway member. The manager deterministically replaces its per-gateway IPv4 table in one transaction, matching the selected tunnel interface, provider internal port, protocol, exact overlay address, and target port. Rule markers bind the lease UID, public-port generation, protocol, and target; the manager reads them back from the exact prerouting and forward chains before the controller reports `GatewayRulesReady=True`. A target-only change does not rotate the provider mapping. A privileged k3s test proves TCP and UDP delivery across a real network-namespace/VXLAN topology, atomic addition and removal of a second identity, stale-rule blocking while the removed target listener remains alive, continued delivery for the unaffected identity, and preservation of unrelated nftables state. ADR 0014 records this boundary.

Neutral renewable delivery from ADR 0011 is implemented. The Pod controller writes a deterministic, versioned document containing only current exact-UID lease records into the existing allocation ConfigMap and patches a content digest annotation to prompt projected-volume refresh without restarting the workload. Admission optionally selects one application container with `networking.waycloak.io/port-forward-container`; that container receives a separate read-only ConfigMap projection containing only `port-forward-leases.json`, never the allocation internals, a service-account token, or added capabilities. The agent rejects malformed or expired records, serves the current document on Pod loopback, and exposes an identity-specific readback on its health port. `Delivered=True` and lease `Ready=True` require the controller to observe the exact Pod UID, lease UID, generation, and canonical expiry from that agent; ConfigMap publication alone remains insufficient. Unit tests, real-API envtest, and the packaged-image k3s test prove filtered disclosure, whole-second Kubernetes timestamp canonicalization, and live generation-1 to generation-2 refresh through both file and loopback surfaces.

The generic provider-assigned application-port handoff is now implemented. `PortForwardLease.spec.target.applicationPortMode` defaults to `Fixed`; `ProviderAssigned` delivers the current public port and requires an exact generation/port acknowledgement before `Delivered=True`. The Pod agent accepts acknowledgements only for a current unexpired Pod-UID-bound record, installs the stable-target-to-application-port redirect with native nftables, and exposes applied state only after kernel repair. The gateway installs exact source NAT from the UID-bound overlay/application tuple to a stable provider internal port allocated from 49152-65535, followed by its ordinary tunnel masquerade. A real k3s network-namespace/VXLAN test proves inbound TCP/UDP DNAT, outbound source address and port translation, public-port rotation, and add/remove stability without renumbering another lease. ADR 0016 records the boundary.

qBitTorrent 5.2.3 is the first evidence-backed application exception. A compatibility test proved that it accepts PCP mapping `6881` to external `42000` but still announces `port=6881` to an HTTP tracker, so a generic PCP surface cannot hide a differing Proton port. The separately packaged unprivileged adapter uses qBitTorrent's loopback API, verifies its listener, and acknowledges the neutral lease generation; it has no Kubernetes token, VPN credential, or Linux capability. A real k3s test proves generation 1 to generation 2 listener rotation, exact tracker advertisement, unchanged Pod UID, and removal of the stale listener. The release workflow now builds, scans, signs, attests, and records this fourth image, and the official example declares `ProviderAssigned` while keeping provider churn out of qBitTorrent's consumers.

Phase 4 is not complete. A gated real-provider harness now codifies the
remaining acceptance against a release-manifest-pinned installation. It uses an
ordinary Pod as the external probe, requires independent TCP and UDP success,
observes exact qBitTorrent tracker advertisement and DHT health, requires both
renewal and actual provider port rotation without a Pod restart, then deletes
the serving gateway and proves protected egress plus both stale ingress
protocols fail before recovery. The harness never reads or prints the VPN
Secret or public endpoint values, and its loopback observer is explicitly a
test fixture rather than provider evidence.

PR #10 merged the harness into `main`. PR #15 made the official qBitTorrent
example a release artifact rendered from the exact adapter digest, and PR #18
added bounded retry for transient keyless Cosign transport failures without
weakening digest, certificate-identity, issuer, or fail-closed verification.
The protected tag workflow published
[`v0.2.0-alpha.2`](https://github.com/Amoenus/waycloak/releases/tag/v0.2.0-alpha.2)
from main commit `9dfbb4ebc3ab08971871e3dd664fc8a51e5c8449`. Release run
[29298122220](https://github.com/Amoenus/waycloak/actions/runs/29298122220)
passed exact-source unit, race, vet, static analysis, envtest, full Kind,
Gitleaks, Trivy, multi-architecture publication, Cosign signing, SPDX
attestation, GitHub provenance, signed-manifest verification, release-file
attestation, and pre-release creation. Its manifest records the immutable
qBitTorrent adapter alongside the controller, agent, gateway manager, and
chart. The released example contains that exact adapter reference once and no
placeholder or mutable image reference. Independent post-release verification
matched the downloaded manifest, signature bundle, and example hashes to
GitHub metadata and verified their GitHub provenance attestations. PR #12 made
GitHub pre-release classification deterministic for alpha, beta, and release
candidate tags.

The `v0.2.0` boundary is now frozen as an OCI adoption release. Forced sustained
provider rotation, formal qBitTorrent DHT certification across that rotation,
and Bitmagnet/Loadstone consumption are versioned `v0.3.0` compatibility work
under issues #4 and #5 rather than open-ended expansion of this release. The
accepted scope is documented in `docs/product/release-scope-v0.2.md`.

The optional KCL OCI module and release-manifest schema `1.1.0` are complete.
The generated module is built from the same CRDs embedded in the Helm chart;
the release workflow packages, scans, signs, attests, provenance-verifies, and
consumes it through an ordinary external KCL module before publication. The
first alpha.4 attempt correctly stopped after its library package was pushed
but then invoked as a root program; no GitHub release was created and the
failed Git tag was removed. The alpha.6 workflow verifies the package through
the same import path a consumer uses.

The homelab GitOps review also found that a static externally supplied webhook
CA could not rotate declaratively. The chart now has an optional cert-manager
mode that creates a namespaced self-signed serving certificate and requests CA
injection while preserving the plain-Kubernetes external Secret/static-CA
default. Helm lint, deterministic rendering, CI, Kind, and live cert-manager
issuance on Kubernetes 1.36 passed. The complete alpha.6 release was published,
independently verified, and installed through Argo CD with two healthy
controllers and cert-manager-injected fail-closed webhooks. Its parallel
gateway deployment exposed a zero-member bootstrap cycle: the manager required
a member route before it could create the overlay, while the staged rollout
correctly required the gateway to become healthy before migrating a workload.
The alpha.7 fix selects the Pod's main-table IPv4 default underlay only while
membership is empty, retains deny-first VXLAN ingress, and switches back to the
observed member route once one exists. The regression passes inside the live
Gluetun network namespace. Issue #29 now advances on alpha.7. Ordinary protected
egress, DNS, provider-port delivery, qBitTorrent operation, and fail-closed
gateway loss in that real deployment remain the candidate acceptance boundary
before final `v0.2.0`. The alpha.7 gateway then reached observed tunnel,
overlay, and DNS readiness, exposing a second stale pre-Phase-4 boundary: the
controller still forced `PortForwardReady=False/PortForwardNotImplemented`.
Alpha.8 removes that obsolete state, makes manager readiness include provider
and gateway-rule reconciliation errors, and reports enabled port forwarding
ready only from the serving manager container's composite observation. The
first real qBittorrent cutover then failed closed before Pod creation because a
Deployment Pod's generated name is finalized after mutating admission, while
the allocation marker had been derived from the pre-final name. Alpha.9 derives
the unique marker from the admission request identity, persists it on the Pod,
and has the validating webhook and controller consume that marker while the
created ConfigMap remains controller-owned and bound to the final Pod UID. The
verified alpha.9 homelab rollout proved that path with a real Deployment Pod:
the controller created the UID-bound allocation and `VPNWorkload`, and the Pod
remained fail-closed in init. That rollout then exposed that gateway
reconciliation never populated the observed serving Pod endpoint in
`VPNGateway.status.overlay`, so the allocation correctly carried an empty
endpoint and the agent refused to configure routing. Alpha.10 publishes the
serving Pod IP with the owned VXLAN and health ports, and clears those fields
when no serving Pod exists. The verified alpha.10 rollout then passed
`waycloak-prepare` and exposed the next symmetric Gluetun firewall boundary:
member packets reached the gateway VXLAN, but Gluetun's local `OUTPUT DROP`
discarded kernel-generated UDP/4789 return encapsulation. Alpha.11 adds only
that protocol-and-port-scoped output handoff while retaining Gluetun ownership
of all other local output. ADR 0009 records the boundary and issue #46 tracks
the release-blocking observation.

The verified alpha.11 homelab rollout proved the complete VXLAN request and
return path: a generated qBitTorrent Pod completed both injected startup gates,
the provider lease and gateway rules became observed ready, and the released
adapter consumed the renewed Pod-local lease document. The real Pod also
exposed a Kubernetes admission-order security gap. Although Waycloak set
`automountServiceAccountToken=false`, the service-account admission plugin had
already injected its default projected token volume and mounts into application
containers before the mutating webhook ran. Alpha.12 structurally removes only
that default projection and its mounts, rejects any other explicit projected
service-account token, and advances the deterministic injection contract to
`v1alpha2`. Unannotated Pods remain untouched. Homelab-only shell quoting and
loopback readiness-probe findings are tracked and fixed in that consumer rather
than productized as workload-specific Waycloak behavior. The same rollout
proved that `Preserve` had no public way to supply the cluster CIDRs already
supported by the agent, preventing the controller from reaching exact agent
delivery readback. Alpha.12 adds validated gateway-level IPv4 CIDRs, publishes
them deterministically in the UID-bound allocation, and keeps RBAC free of
cluster-wide Node discovery. The alpha.12 homelab rollout then reached a fully
Ready lease and credential-free `v1alpha2` Pod, but its protected-egress
baseline found that the preserved Service CIDR selected the main routing table
before gateway-DNS redirection could select the protected table. Direct VPN IP
transport and direct overlay DNS both worked. A live policy-rule proof restored
Kubernetes-resolver DNS immediately. Alpha.13 makes those owned UDP/TCP port-53
rules precede Preserve CIDRs, and the Linux acceptance fixture now places its
DNS target inside a preserved CIDR so that topology cannot regress.

The signed alpha.13 bundle was published from `main` and independently
verified across every release-asset digest, OCI signature and provenance
attestation, release-file attestation, and the release-manifest Sigstore
bundle. Its first concurrent control-plane and workload rollout exposed that a
zero-unavailable webhook Service can still admit a Pod through an old replica,
producing a mixed release identity even though each individual mutation is
deterministic. The supported upgrade procedure now rolls and verifies the
control plane before recreating protected Pods; issue #55 owns an observed
generation gate.

The alpha.14 homelab acceptance used the same ESO-generated Proton credential
Secret as the replaced PoC without reading or printing either value. An
initial healthy OpenVPN tunnel could not acquire a lease because the provider
driver sent NAT-PMP to a fixed peer even though Proton had assigned a different
tunnel subnet. PR #59 now derives the peer from the observed OpenVPN interface
prefix while retaining an explicit test/operator override. The signed
alpha.14 bundle was published from `main` by release run 29352248913 after the
full source, race, static, envtest, Kind, vulnerability, OCI, signing, SBOM,
and provenance gates passed.

The controlled alpha.14 rollout first converged both webhook/controller
replicas, then activated the `OnDelete` singleton gateway. Gateway replacement
regressed observed readiness immediately, protected traffic remained fail
closed, and an unannotated control retained ordinary HTTPS egress. After the
documented protected-Pod roll, a fresh UID-bound allocation completed both
startup gates; the manager acquired and renewed a real Proton lease through
the dynamically derived peer; every gateway and lease condition became True;
and qBitTorrent became 3/3 Ready without capabilities, a Kubernetes API token,
or credential access. Protected DNS and HTTPS, ordinary HTTPS, the public
qBitTorrent route, and Qui all returned successfully, followed by a clean Qui
health window with no new timeouts. The remaining v0.2.0 work is publication
of the final signed bundle and replacement of the candidate pins with those
final immutable identities.

[`v0.2.0`](https://github.com/Amoenus/waycloak/releases/tag/v0.2.0) is now the
completed OCI adoption release. Protected release run
[29355117236](https://github.com/Amoenus/waycloak/actions/runs/29355117236)
published it from main commit `986ade16903682c4087c8989b638a3a1310ce119`.
Independent verification matched all 12 GitHub release-asset hashes, all six
OCI signatures and SPDX attestations, all six OCI provenance attestations, all
12 release-file provenance attestations, and the signed release-manifest
bundle. It also confirmed four Linux amd64/arm64 image indexes, three embedded
Helm CRDs, deterministic Helm rendering, and consumption of the optional KCL
module through an external OCI dependency.

Homelab completed the documented two-phase rollout: final controllers and the
operator-activated singleton gateway converged before the protected workload
was rolled to the final agent and qBitTorrent adapter digests. The final Pod is
3/3 Ready, all gateway and lease conditions are True, the application has no
added capability or API credential, protected and ordinary HTTPS both
succeed, and the qBitTorrent and Qui public routes are healthy. A subsequent
real provider renewal preserved the Pod UID and produced no new Qui health
errors. Phase 5's precise next vertical slice is sustained real-provider
rotation plus qBitTorrent tracker/DHT certification under issue #4; automatic
same-Pod recovery after singleton replacement remains versioned operational
maturity work under issue #61.

The `v0.2.1` patch candidate hardens the qBitTorrent adapter after live
adoption exposed a false-positive delivery acknowledgement: the application
preference could report the assigned port while no BitTorrent listener was
active. The adapter now requires a Pod-local TCP listener before acknowledging
the lease, keeps readiness false when that listener is absent, rate-limits
unchanged pending logs, and logs recovery transitions. Unit coverage and the
real qBitTorrent Kind rotation test verify the behavior. The deployment-side
stale native interface binding was repaired and qBitTorrent returned to
`connected` with TCP/UDP listeners on the current provider port. Gateway
endpoint rollover and lease/readiness bootstrap findings remain explicitly
open under issues #70 and #71; this patch does not claim to solve them.

## Release progression

`v0.1.0` delivered the first usable private-egress foundation: a single shared
Gluetun gateway, injected VXLAN agent, fail-closed egress, standard Kubernetes
Secret references, and observable gateway status.

The `v0.2.0` release adds provider-neutral `PortForwardLease`, Proton
NAT-PMP, stable gateway translation, renewable UID-bound delivery, the narrow
qBitTorrent adapter, signed OCI Helm and optional KCL publication, and real
homelab adoption. `v0.2.1` is the listener-observation and adapter-log hardening
patch. `v0.3.0` is the sustained real-provider tracker, peer-ingress, DHT,
rotation, and additional-workload certification milestone.

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
