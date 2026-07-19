# Project status

Last updated: 2026-07-19

## Current phase

[`v0.3.0`](https://github.com/Amoenus/waycloak/releases/tag/v0.3.0) is the
completed provider-and-workload compatibility release. Protected release run
`29689814556` published it from main commit `b72ba721`; independent verification
matched the signed release manifest and all seven digest-addressed OCI
attestations. Homelab PR #1464 promoted those exact final identities in merge
`b2987679`, and Argo CD reports the root, Waycloak, qBitTorrent, and Bitmagnet
applications Healthy/Synced.

The final homelab runtime has two Ready controllers, exactly one 2/2 Ready
Proton/OpenVPN gateway, and 3/3 Ready qBitTorrent and Bitmagnet workloads on the
verified final manager, agent, and adapter digests. Both production port-forward
leases are Ready. The exact-final real-provider gate passed in 1229.32 seconds,
proving sustained renewal, actual endpoint rotation, qBitTorrent reannounce,
DHT and external TCP/UDP ingress, fail-closed gateway loss and stale ingress,
and same-Pod recovery without creating a competing provider session. Cleanup
left zero acceptance resources.

The v0.4 research selected a developer-preview outcome and E2 architecture: an
optional chained CNI creation-time handoff installs a Pod-parent cgroup eBPF
deny boundary, and a prepared-node agent adopts and reconciles it. The current
Pod-local nftables/netlink sidecar remains the supported default. The preview is
explicit, restricted to operator-prepared and executable-probed nodes, and
never silently falls back. The evidence, rejected alternatives, target-runtime
boundary, benchmarks, and cutoff are recorded in `V0.4.0_GOAL.md`,
`docs/research/ebpf-data-plane.md`, the v0.4 release PRD, and ADR 0024.

Implementation begins with the containerd CNI identity handoff and node-agent
ownership proof. `v0.4.0` is not releasable merely because eBPF attaches: the
node path must own a complete declared feature subset, remove the privileged
networking sidecar or demonstrate another accepted material benefit, pass the
backend-neutral fail-closed suite on amd64 and arm64, and support safe CNI
installation and rollback. ADR 0006 remains the production decision until
those developer-preview gates pass.

The implementation graph is tracked by epic #6 and ordered issues #107-#114:
contract, executable CNI handoff, node eBPF lifecycle, complete node networking
ownership, admission/status, prepared-node packaging, equivalent measurement,
and signed mixed-mode homelab certification. Research issues #65 and #34 remain
the source evidence rather than implementation catch-alls.

RC1 fixed the long-name StatefulSet lookup
defect exposed by the signed alpha.6 real-provider harness (#96), and its live
GitOps rollout preserved fail-closed gateway replacement while aligning the
controller, agent, manager, qBitTorrent adapter, Bitmagnet adapter, and tested
Gluetun digests. The first RC1 acceptance run then selected an amd64 worker
with independently reproduced asymmetric Pod-CIDR reachability: traffic from
another worker to the acceptance Pod timed out while the reverse direction
succeeded. The protected Pod correctly remained NotReady and the run cleaned
all temporary resources. RC2 adds a validated, operator-selected Ready amd64
node for this destructive gate so certification can target a reviewed cluster
path without changing production scheduling or any runtime readiness rule.
The signed RC2 gate then exposed the provider account's concurrent-session
boundary: a temporary second Gluetun instance repeatedly received
authentication failures while the reviewed production gateway held the same
credential session. RC3 adds an explicit existing-gateway acceptance mode. It
requires that gateway to be observed Ready, use the manifest-tested Gluetun
digest, and enable Proton NAT-PMP; it preserves isolated acceptance workloads
and leases, and still replaces the serving gateway Pod to prove fail-closed
loss and observed recovery without creating a competing provider session.
The first RC3 existing-gateway run reached an observed Ready real-provider
lease, then exposed a test-Pod probe mismatch: qBitTorrent correctly bound its
WebUI to loopback, while the Kubernetes TCP probe targeted the Pod IP and could
never succeed. RC4 changes only that application probe to execute against the
actual loopback endpoint and adds a focused contract test; Waycloak gateway,
lease, agent, and adapter readiness rules are unchanged.
The signed RC4 run then reached real ingress and a fully Ready Pod/lease but
reported zero DHT nodes for the full acceptance window. Runtime isolation
showed that generic protected UDP and lease-port UDP both succeeded, while
qBitTorrent opened DHT sockets on both the Kubernetes Pod address and the
Waycloak overlay. Its bootstrap selected the Pod-address path, which the
gateway correctly rejected rather than weakening the overlay-source invariant.
RC5 makes the unprivileged qBitTorrent adapter discover the single Waycloak
interface, apply its exact name and IPv4 address through the loopback-only API,
and restart DHT only when an enabled DHT is rebound. It also makes the
disposable fixture's DHT setting explicit and tests the idempotent binding and
restart contract.

RC5 then passed the full source-level real-provider acceptance. RC7 corrected
the chart and KCL release metadata, published the exact reviewed images and
packages, and reached Healthy/Synced production state. Its fail-closed rollout
withdrew both production leases before replacing the singleton gateway and
restored them only after the replacement manager observed the data plane.

The exact RC7 ingress gate exposed a provider compatibility boundary rather
than a routing fallback: the NAT-PMP external address was a valid global IPv4
address but differed from the tunnel's ordinary outbound source address.
Waycloak recorded only the NAT-PMP port, so the harness probed the wrong
address and qBitTorrent had no correct address to report to trackers. RC8
carries the provider-observed public address through gateway
observation, `PortForwardLease` status, the neutral adapter record, and
qBitTorrent's `announce_ip`; an address or port change advances the lease
generation and regresses downstream readiness until reapplied. The tracker
acceptance records only a hash of the announced address while proving it
matches the lease endpoint.

The same run accidentally omitted RC3's existing-gateway selector and created
a second temporary Gluetun/OpenVPN Pod beside production. The provider account
had already demonstrated this concurrent-session authentication boundary in
RC2. The harness now rejects creation when another `VPNGateway` references the
selected credential Secret and directs the operator to
`WAYCLOAK_REAL_VPN_GATEWAY`, ensuring final certification uses one provider
session.

The approved RC8 activation replaced only the existing singleton gateway and
then recreated the two protected workloads to inject the exact RC8 agent. The
manager correctly failed closed when both replacement Pods were allocated the
same overlay address. Allocation reconciliation was already single-threaded,
but it selected from the eventually consistent informer cache; the second
reconcile could therefore miss the first workload's just-persisted status and
reuse its address. RC9 selects from the API server's authoritative workload
list while retaining single-threaded allocation and stable identities. A
regression test presents a deliberately stale cached list and proves the next
address still accounts for the durable authoritative allocation. Exact RC9
real-provider certification then exposed a separate convergence defect.
After a short provider mapping expired and was reacquired, provider observation
recovered but `GatewayRulesReady` remained false for the full bounded wait and
another renewal. Rule generation depended on controller status returning to
the manager through a kubelet-projected ConfigMap, whose delay can match the
provider lifetime. ADR 0023 moves mapping-generation ownership and matching
rule reconciliation into the gateway manager's local loop. Same-endpoint
expiry renewal preserves generation; endpoint rotation or reacquisition
advances it; transient renewal failure exposes `renewalPending` while the old
observation remains valid; expiry or tunnel loss removes rules fail closed.
Exact candidate real-provider certification remains required before final
`v0.3.0`.

RC10 passed source CI, signed publication, independent artifact verification,
and exact-digest homelab deployment. Its sustained live run preserved one
provider session, held generation stable across expiry-only renewals, and
recovered generation 1 to 2 after replacement of the singleton gateway. The
new mapping, matching gateway rules, delivery, application acknowledgement,
and external ingress all became Ready without replacing the qBitTorrent Pod.
The remaining assertion timed out because existing torrents continued to
advertise the previous endpoint: the adapter updated qBitTorrent's listener and
`announce_ip`, restarted DHT, and acknowledged the generation, but never called
qBitTorrent's torrent reannounce API. The next candidate makes successful
reannounce part of the application acknowledgement boundary for actual
advertised-endpoint changes while leaving expiry-only renewal idempotent.

RC11 made that application boundary generation-aware and passed the complete
20-minute real-provider gate against the existing singleton Proton/OpenVPN
session. The run proved sustained expiry-only renewal, forced actual mapping
rotation, immediate tracker reannouncement, DHT and real TCP/UDP ingress,
unchanged workload Pod UID, a separate destructive gateway-loss event,
fail-closed protected egress and stale ingress, and full same-Pod recovery. It
created no competing gateway, logged no endpoint or credential value, cleaned
its isolated resources, and left the exact-digest RC11 production gateway,
qBitTorrent, Bitmagnet, and both leases Healthy/Ready. Final `v0.3.0` publication,
independent verification, and exact-digest homelab promotion are the remaining
release steps.

The `v0.3.0-alpha.6` candidate addresses live issues #90, #92, and #94. A sustained Gluetun
DNS/tunnel health failure correctly withdrew composite gateway and protected
workload readiness, but Gluetun remained alive with HTTP 500 health while its
OpenVPN child failed to complete the internal restart. Because the generated
engine container had no Kubernetes probes, recovery required deleting the
singleton gateway Pod. Gluetun gateways now use a loopback exec startup probe,
fast readiness probe, and delayed liveness probe: traffic remains fail closed
immediately, while two minutes of continuous post-startup failure restarts only
the engine container. API-server defaults are also treated as compatible during
StatefulSet reconciliation, eliminating the semantic no-op update loop,
optimistic-concurrency errors, and spurious rollout-required events observed
during the incident. Packaged real-provider failure injection and the homelab
soak completed without replacing gateway or workload Pod identities and without
direct-egress fallback.

The same live gate exposed issue #92: renewal requests reused the last public
port even though Proton advertises that requested external ports are
unsupported. Proton therefore returned a new random port on each 45-second
renewal, repeatedly reconfiguring qBittorrent and forcing Bitmagnet into
restart backoff. The manager now sends a zero external-port suggestion for
both acquisition and renewal whenever the provider capability says requests
are unsupported, while retaining the last public port only for drivers that
explicitly support it. The public lease generation therefore changes only on
an actual provider rotation.

Issue #94 was exposed by the alpha.5 rollout when Gluetun's one-time public-IP
metadata lookup timed out even though OpenVPN, tunnel health, and DNS were
ready. Public-IP metadata is not used by routing, NAT-PMP, DNS, or fail-closed
enforcement, so it is now best-effort telemetry and no longer gates gateway
readiness. Tunnel health and DNS observation remain mandatory.

The `v0.3.0-alpha.3` candidate fixes a real multi-lease starvation found while
adding Bitmagnet beside qBitTorrent (#88). Provider acquisition and renewal now
run against a private reconciliation copy instead of holding the published
observation mutex across network I/O. The manager atomically publishes complete
updates, keeps the last complete observation readable while renewal is in
flight, expires stale observations locally, and publishes removals or mapping
replacements as non-ready before provider release I/O. Unit coverage holds a
provider call blocked while proving bounded snapshot reads and fail-closed
expiry. Real two-client Proton convergence remains an explicit release gate.

The `v0.3.0-alpha.2` candidate adds the second narrow reference workload
adapter under issue #5. The Bitmagnet adapter consumes only the Pod-local,
provider-neutral lease protocol, atomically stages `dht_server.port`, observes
the actual UDP listener in the shared Pod network namespace, and acknowledges
only the exact Pod UID, lease identity, generation, and applied port. Its
separate restart probe coordinates Bitmagnet's restart-coupled configuration
without adding application semantics to the controller or granting the
application Kubernetes credentials, VPN credentials, or Linux capabilities.
The signed release manifest schema advances to `1.3.0` and records the new
multi-architecture adapter artifact and compatibility range. Real deployment
and rotation evidence remains required before issue #5 is complete; Loadstone
validation remains independently open.

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

[`v0.2.1`](https://github.com/Amoenus/waycloak/releases/tag/v0.2.1) hardens
the qBitTorrent adapter after live
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

Protected release run
[29397528292](https://github.com/Amoenus/waycloak/actions/runs/29397528292)
published the signed multi-architecture images, CRD-bearing Helm OCI chart,
optional KCL OCI module, SPDX SBOMs, provenance attestations, and signed
release manifest from main commit
`cb623379f21526f6ce840d32487bb2cdae8eaeae`. Homelab adopted the immutable
adapter digest
`sha256:88a257e0f1a9c393d030addee88d22f4fd5a57ab9a00b6b9b84768893df44472`.
The rollout reproduced the known gateway endpoint transition in #70 and was
recovered without changing the workload Pod UID. The final Pod is 3/3 Ready,
all lease conditions are True, qBitTorrent reports `connected`, TCP and UDP
listen on the provider-assigned port across the Pod and overlay addresses,
and the public route returns successfully. The adapter emitted one pending
state per transition or rate-limit interval and a single recovery event.

The `v0.2.2` reliability patch implements automatic recovery for issue #70.
`VPNGateway` status changes now enqueue every bound workload, the Pod
controller reconciles the complete UID-bound allocation document rather than
treating its routing fields as create-once, and the Linux agent replaces a
Waycloak-owned VXLAN link when its observed remote endpoint or other immutable
attributes no longer match desired state. The application Pod UID, overlay
address, and lease identity remain stable, while existing fail-closed policy
stays installed until the replacement gateway health check succeeds. Unit,
envtest, Linux compilation, privileged drift coverage, and a packaged-image
gateway-loss/replacement lifecycle regression cover the boundary. The shared
cluster harness also records pre-existing CRDs and no longer deletes resources
it did not create.

Protected release run
[29430978558](https://github.com/Amoenus/waycloak/actions/runs/29430978558)
published `v0.2.2` from main commit
`9d0b47d2bfaf9881d75c3851ec3b45f3808d0e08`. The release includes signed
multi-architecture controller, agent, gateway-manager, and adapter images; the
CRD-bearing Helm OCI chart; the optional KCL OCI module; SPDX SBOMs; provenance
attestations; and a signed release manifest. Homelab PR
[`Amoenus/homelab#1427`](https://github.com/Amoenus/homelab/pull/1427) adopted
the release-manifest identities.

The no-intervention production proof deleted the singleton gateway Pod after
establishing an upgraded qBittorrent baseline. DNS and ordinary egress failed
immediately, the agent and adapter became unready, and every lease readiness
component became False. The replacement moved to a different observed underlay
endpoint; the controller updated the existing UID-bound allocation and emitted
`GatewayEndpointUpdated`, and the running agent recovered without a ConfigMap
patch, link deletion, process restart, or application restart. The qBittorrent
Pod UID, overlay address, allocation generation, and lease UID remained stable
with zero container restarts. Proton rotated the public port; the adapter
installed TCP and UDP listeners, all lease conditions returned True, protected
egress succeeded, and the public route returned HTTP 200. Issue #70 is
complete.

The first `v0.3.0` reliability slice is complete. PR
[#80](https://github.com/Amoenus/waycloak/pull/80) closes issues #71 and #75
with a generation-bound qBitTorrent adapter readiness state machine. Initial
lease acquisition no longer depends on application Pod readiness, brief local
API timeouts preserve a previously proven endpoint for a bounded interval,
and lease loss, listener loss, rotation mismatch, API rejection, or sustained
timeouts still withdraw readiness. The Kind acceptance suite proves bootstrap,
transient-stall retention, sustained-stall withdrawal, recovery, rotation, and
tracker behavior; workflow run
[29439966576](https://github.com/Amoenus/waycloak/actions/runs/29439966576)
passed the complete verification, security, review, and Kind gates. This work
is merged on `main` but is not yet a published release.

PR [#81](https://github.com/Amoenus/waycloak/pull/81) completes the second
`v0.3.0` reliability slice and closes #55. Helm now derives a deterministic
admission generation from the immutable controller and agent identities. Each
webhook replica checks the desired generation through an uncached API read for
readiness and again for every opted-in mutating or validating request; stale
replicas reject rather than inject an old agent, while API-server match
conditions keep unannotated Pods outside the failure domain. Injected Pods
record the applied generation, and a 100-percent controller surge prevents the
zero-unavailable rollout from deadlocking when every old replica becomes
unready together. Unit, direct old/new replica, and Helm generation-changing
Kind coverage prove the transition. ADR 0020 records the contract. The next
ordered slice was gateway membership generation in #48.

That third `v0.3.0` reliability slice is now implemented. The controller hashes
canonical stable member identities plus overlay and observed underlay addresses
into a desired generation published in the gateway ConfigMap. The manager
advances a tokenless last-known-good applied generation only after network,
forwarding, gateway-rule, and DNS reconciliation succeeds. Gateway status
exposes both values and reports `MembershipApplied=False` while projection is
pending or observation is stuck or failing, then `MembershipApplied=True` after
the generations converge. It emits transition events and polls while pending;
`OverlayReady` and overall `Ready` remain false until they match. Malformed or
partial projections preserve the previous kernel state and applied generation.
Unit coverage proves add, remove, underlay replacement, stable ordering,
malformed projection, and pending-to-applied transitions. The privileged Kind
gateway test exercises malformed projection retention and add/remove generation
advancement without disrupting an existing allocation. ADR 0021 records the
contract. The next ordered work is engine-native Gluetun configuration in #66.

The fourth `v0.3.0` slice implements the engine-native boundary from #66.
`VPNGateway.spec.engine.config` now imports Gluetun-native non-secret
environment from same-namespace ConfigMaps and mounts ConfigMap or Secret files
read-only only into the engine. Provider, OpenVPN/WireGuard, server filters,
custom-provider paths, non-conflicting DNS, and updater settings no longer need
Waycloak fields. The controller rejects reserved health, control-auth,
interface, firewall, DNS-bind, and competing port-forward keys with stable
redacted reasons; it hashes only non-secret ConfigMap inputs into an opaque
`OnDelete` rollout annotation and never reads native Secrets. The legacy
`provider` object remains mutually exclusive migration compatibility.
Proton NAT-PMP is gated by the effective non-secret provider/protocol and still
requires runtime lease observation. Unit, envtest, generated CRD/KCL, example,
and Kind coverage exercise Proton/OpenVPN, Mullvad/WireGuard, custom OpenVPN,
reserved conflicts, ConfigMap rotation, migration skew, and engine-only Secret
projection. ADR 0022 records the concrete projection contract.

The v0.3 workload-adapter extension boundary is now implemented. The public
`networking.waycloak.io/adapter/v1alpha1` HTTP/JSON contract publishes schemas
and portable current, rotated, expired, missing, duplicate, wrong-Pod-UID, and
stale-generation vectors. A cluster-scoped `WorkloadAdapter` records an
operator-approved immutable digest and protocol; protected Pod templates
separately select that trust record and an existing sidecar. Admission requires
an exact image match, readiness probe, non-root/read-only execution, seccomp,
no added capability, hostPath, hostPort, device, or projected API token, and
supplies only reserved protocol/loopback environment. A standard-library-only
Python sample proves authors need no Waycloak Go internals. The qBitTorrent
reference adapter posts Pod-UID/lease-UID/generation/port-exact
acknowledgements, carries compatibility OCI labels on both release platforms,
and remains an independently signed artifact. Helm and generated KCL include
the trust API, while the release manifest records the protocol, reference
compatibility, and attested conformance-kit asset. The next ordered v0.3 work
is sustained real-provider certification and additional workload adoption.

PR [#85](https://github.com/Amoenus/waycloak/pull/85) aligns the destructive
real-provider release gate with the v0.3 native engine contract. The harness
now creates a non-secret Proton/OpenVPN ConfigMap, mounts the dedicated
`username`/`password` Secret read-only only into Gluetun, and rejects any
unexpected Secret key shape without reading a value. It retains the real TCP
and UDP ingress, tracker, DHT, renewal, provider rotation, unchanged Pod UID,
gateway-loss, stale-ingress, and recovery assertions. All unit, race, envtest,
generated-artifact, security, and Kind acceptance gates passed on the reviewed
main-contained change. The chart and optional KCL module are now versioned
`0.3.0-alpha.1` for the signed certification candidate. This version change is
not evidence for issue #4 by itself: #4 remains open until the signed candidate
is installed and the gated real-provider run succeeds without publishing
credentials or endpoint values.

## Release progression

`v0.1.0` delivered the first usable private-egress foundation: a single shared
Gluetun gateway, injected VXLAN agent, fail-closed egress, standard Kubernetes
Secret references, and observable gateway status.

The `v0.2.0` release adds provider-neutral `PortForwardLease`, Proton
NAT-PMP, stable gateway translation, renewable UID-bound delivery, the narrow
qBitTorrent adapter, signed OCI Helm and optional KCL publication, and real
homelab adoption. `v0.2.1` is the listener-observation and adapter-log hardening
patch, and `v0.2.2` adds automatic same-Pod recovery after gateway endpoint
replacement. `v0.3.0` begins with admission, membership-observation, and
adapter-readiness hardening, then delivers engine-native Gluetun configuration,
the workload-adapter protocol, sustained real-provider tracker/peer-ingress/DHT
and rotation proof, and additional-workload certification.

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
