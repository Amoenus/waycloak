# Implementation roadmap

Each phase ends with observable acceptance criteria. A fresh implementation agent should take the first unchecked vertical slice, not build all packages speculatively.

## Phase 0 — repository and design baseline

- [x] Product PRD and developer experience.
- [x] Architecture, networking, and threat model.
- [x] Proposed API contract.
- [x] Test and release requirements.
- [x] Homelab prototype provenance.
- [x] Resolve remaining open API questions through ADRs (ADRs 0006, 0010, and 0011).
- [x] Scaffold Go module, controller-runtime project, and generated CRDs.

Exit: `go test ./...` runs on a minimal controller scaffold and generated manifests are reproducible.

## Phase 1 — admission and stable registration

- [x] Define `VPNGateway` and internal `VPNWorkload` Go APIs.
- [x] Reconcile stable address allocations with deletion quarantine.
- [x] Implement idempotent mutating admission for annotated Pods.
- [x] Implement annotated-but-uninjected rejection.
- [x] Publish precise conditions and events.
- [x] Prove unannotated Pods are unchanged in unit tests.
- [x] Prove admission, startup blocking, restart stability, membership stability, authorization, and webhook-outage behavior in a Kind-compatible cluster suite.

Exit: verified on Kubernetes 1.36 k3s; the same suite defaults to a disposable Kind context and shows injected structure, fail-closed admission outage behavior, and durable allocations across controller restart and unrelated membership changes.

## Phase 2 — fail-closed data plane

The deny-first agent, DNS containment, and exact packaged-image lifecycle slices are complete. The fake gateway is test-only and does not constitute a production VPN data plane.

- [x] Build minimal non-root-where-possible agent image.
- [x] Install owned nftables policy before application startup.
- [x] Establish and monitor VXLAN to a test gateway.
- [x] Implement route and firewall drift repair.
- [x] Implement cluster-local policy modes.
- [x] Implement gateway-routed DNS.
- [x] Add preflight diagnostics.
- [x] Prove the full injected-Pod lifecycle with the packaged image and fake gateway.

Exit: passed on Kubernetes 1.36 k3s. Forced agent and gateway failures produce no direct external packets, service DNS works according to policy, the exact injected image reports observed readiness, and an unannotated replacement retains ordinary networking.

## Phase 3 — Gluetun gateway (`v0.1.0`)

The functional gateway path is complete. The controller-owned singleton StatefulSet, headless Service, read-only engine configuration, gateway-manager runtime, pinned Gluetun adapter, stable membership publication, native gateway VXLAN, deny-first forwarding/NAT, and split-DNS proxy are implemented. This includes digest-only images, engine-only credential mounting, token isolation, owner cleanup, generated RBAC, typed tunnel/DNS/public-IP observations, observation-driven component status, exact cluster-DNS firewall/routing exceptions, and manager-owned VXLAN source authorization. Fixture tests remain explicitly non-VPN. A gated real-provider k3s acceptance proves distinct protected public egress through the production path, Kubernetes DNS containment, UID-gated startup, credential isolation, and fail-closed behavior after abrupt gateway deletion without exposing Secret or public-IP values. The protected `v0.1.0` workflow published and independently verified signed multi-architecture images, the signed Helm chart, SPDX SBOM attestations, GitHub provenance, and the signed release manifest. Next vertical slice: the provider-capability interface and `PortForwardLease` API.

- [x] Reconcile gateway StatefulSet, Service, configuration, and RBAC.
- [x] Add gateway and controller/webhook disruption controls without cloning the singleton tunnel.
- [x] Integrate pinned Gluetun engine and prove the production protected-Pod path against a real provider.
- [x] Implement tunnel and public-egress health observations.
- [x] Apply membership incrementally without tunnel restart.
- [x] Add a deterministic Helm chart and multi-architecture controller image build.
- [x] Implement the pinned, keyless image/chart publication pipeline, SBOM/provenance gates, and signed release-manifest tooling.
- [x] Execute the protected tag workflow and verify the published OCI artifacts.
- [x] Publish install, security-exception, troubleshooting, and uninstall guides.
- [x] Prove zero-unavailable Helm upgrade/rollback, two-phase webhook certificate rotation, and operator-activated singleton gateway rollouts.

Exit: e2e acceptance proves annotated VPN IP, unannotated normal IP, fail-closed outage, DNS containment, and credential isolation on Kind and k3s/k3d.

## Phase 4 — port forwarding (`v0.2.0`)

- [x] Define provider capability interface and `PortForwardLease` API.
- [x] Implement Proton NAT-PMP driver through the tunnel.
- [x] Persist stable lease identities and generations.
- [x] Reconcile TCP/UDP DNAT atomically.
- [x] Deliver neutral lease records to workloads.
- [x] Implement the generic exact-generation `ProviderAssigned` handoff and
  evidence-backed qBitTorrent sidecar outside application-agnostic controller
  semantics, with protocol-faithful local/k3s evidence only.
- [x] Publish the signed adapter image from a main-contained tag, record its
  immutable reference in the signed `v0.2.0-alpha.2` release manifest, and
  publish the official example with that exact digest and no placeholder.
- [x] Publish the complete signed OCI bundle, including the CRD-bearing Helm
  chart and optional KCL module recorded in the release manifest.
- [x] Replace the originating homelab PoC with the immutable release candidate
  and resolve findings that block ordinary protected operation.
- [x] Publish final `v0.2.0` from a main-contained signed tag.
- [x] Require the qBitTorrent reference adapter to observe its active TCP
  listener before acknowledging provider-assigned delivery, with state-aware
  logs and real-image rotation coverage (#68).
- [x] Publish the signed `v0.2.1` patch bundle and update the real deployment
  to its immutable adapter digest.
- [x] Publish the signed `v0.2.2` reliability patch and prove automatic
  same-Pod recovery after a singleton gateway endpoint replacement (#70).

Exit: the signed OCI candidate replaces the PoC; qBitTorrent uses Waycloak for
protected egress and provider-port delivery during ordinary operation; the
gateway remains fail closed; and the verified final bundle is published.

## Phase 5 — provider and workload compatibility (`v0.3.0`)

The final `v0.3.0` release is published, independently verified,
GitOps-deployed, and real-provider certified. The alpha.6 deployment completed engine auto-healing and stable
renewal validation. RC1 fixed the long-name StatefulSet lookup exposed by the
first full harness run. Its next run proved the startup deny gate but selected
a worker with independently reproduced asymmetric Pod-CIDR reachability. RC2
adds a validated reviewed-node override for the destructive gate; the complete
gate must still pass from reviewed `main` without relaxing runtime readiness.
RC2 then proved the provider rejects a temporary second engine using the same
active credential session. RC3 adds a strictly validated existing-gateway mode
so the gate can reuse that reviewed provider session while retaining isolated
workload/lease resources and the destructive gateway-loss assertion.
That path reached a real Ready lease but exposed a harness-only qBitTorrent
probe mismatch: the WebUI was intentionally loopback-bound while the Pod probe
targeted its Pod IP. RC4 probes the actual loopback endpoint without changing
any Waycloak readiness condition.
RC4 subsequently reached real ingress and a fully Ready Pod/lease, but
qBitTorrent DHT selected a socket bound to the Kubernetes Pod address instead
of its Waycloak overlay address. The gateway correctly dropped that source.
RC5 makes the qBitTorrent adapter bind the application to the single observed
Waycloak interface/address, restart an enabled DHT only when that binding
changes, and explicitly enable DHT in the disposable fixture. Focused tests
protect the idempotent binding, restart, and fixture contracts.
RC7 reached the exact real-provider path and proved that Proton's NAT-PMP
external address can differ from the tunnel's ordinary outbound source
address. RC8 therefore publishes the provider-observed address
with the port, advances generation when either changes, configures and verifies
qBitTorrent's tracker announce address, and probes ingress at that exact
endpoint. The harness also rejects a second gateway that references credentials
already used by a namespaced `VPNGateway`; final certification selects the
existing production gateway and retains the destructive fail-closed replacement
assertion without opening a competing OpenVPN session.
Its approved activation then exposed an allocation race during simultaneous
workload replacement: single-threaded reconciliation selected addresses from
an eventually consistent cache and could miss the immediately preceding
durable status write. The gateway rejected the duplicate membership and stayed
fail closed. RC9 uses an authoritative API-server read for allocation while
retaining single-threaded, stable-identity semantics. Its instrumented renewal
run then proved that feeding controller-derived mapping generation back through
a mounted ConfigMap could leave gateway rules perpetually behind a short-lived
provider lease. RC10 implements ADR 0023: the gateway manager owns mapping
generation and matching local rule convergence, expiry-only renewal preserves
generation, and expiry still removes rules fail closed. RC10 then exposed that
qBitTorrent changed its listener and announce address without immediately
reannouncing active torrents. RC11 makes successful generation-bound tracker
reannounce part of application acknowledgement. Its complete sustained
real-provider run passed renewal, actual rotation, ingress, advertisement, DHT,
gateway loss, fail-closed behavior, and same-Pod recovery. The same gate passed
again against exact final artifacts, which were published, independently
verified, promoted, and observed Healthy/Ready in the homelab.

- [x] Eliminate the adapter readiness bootstrap cycle while keeping genuine
  lease and listener loss fail closed (#71).
- [x] Preserve a previously proven qBitTorrent Service endpoint across bounded
  transient local API timeouts, then withdraw it on sustained failure (#75).
- [x] Add an observed admission release/generation gate that prevents mixed
  injected agent identities during zero-unavailable webhook upgrades (#55).
- [x] Expose desired and last-known-good applied gateway membership generations
  without weakening malformed-projection handling (#48).
- [x] Keep published lease observations readable and expiry-aware while slow
  provider renewal I/O is in flight (#88).
- [x] Publish and deploy engine-container auto-healing, then prove that a
  sustained Gluetun health failure remains fail closed and restores the same
  gateway/workload Pod identities automatically (#90).
- [x] Complete sustained real-provider qBitTorrent ingress, advertisement,
  DHT, renewal, and actual rotation certification using the existing gated
  harness.
- [x] Validate Bitmagnet consumption of the neutral lease contract. The
  deployed adapter stages provider-assigned DHT ports, observes the UDP
  listener, acknowledges exact generations, and recovered Ready across real
  gateway replacement. Loadstone validation is outside the revised v0.3.0
  cutoff and remains future compatibility work.
- [x] Record additional provider/application compatibility and troubleshooting
  evidence from real deployments.
- [x] Replace provider-shaped gateway convenience fields with engine-native
  Gluetun configuration and a documented migration (#66).
- [x] Publish the workload-adapter protocol, trusted selection mechanism,
  conformance kit, and qBitTorrent reference implementation (#67).

Exit: final `v0.3.0` is deployed in the homelab; qBitTorrent survives provider
renewal or rotation without Pod replacement, and Bitmagnet has a documented,
real-deployment-proven narrow integration. Loadstone remains future work.

## Phase 6 — eBPF research and v0.4.0 definition

Research precedes the release PRD. eBPF is a focused hypothesis, not a selected
backend. ADR 0006 remains the only supported production data-plane decision.
The intended compatibility model is additive: the existing Pod-local mode stays
the default, while any future eBPF mode is explicit and restricted to
operator-prepared, capability-verified nodes with no silent fallback.

- [x] Map the as-built filter, VXLAN, routing, DNS NAT, port NAT, verification,
  privilege, and injected-component responsibilities (#65).
- [x] Collect initial amd64/arm64 homelab kernel, cgroup, BTF, bpffs, hook, map,
  and Flannel evidence (#65).
- [ ] Complete primary-source research for attachment, persistence, replacement,
  privilege, verifier, portability, CNI ownership, and sidecarless models (#65).
- [ ] Resolve Pod cgroup and host-veth identity/lifecycle on k3s/containerd with
  disposable, non-production probes (#65).
- [ ] Test the minimum deny-only cgroup and tc/TCX prototypes needed to decide
  feasibility; do not carry production traffic (#65).
- [ ] Measure equivalent-backend performance, scaling, total node/Pod resource
  use, startup/recovery, and injected-component footprint (#34).
- [ ] Publish the architecture comparison, threat-model delta, support boundary,
  recommendation, and rejected alternatives (#65, #34).
- [ ] Derive and review the `v0.4.0` PRD, release cutoff, follow-up ADR direction,
  conformance requirements, and ordered GitHub issue graph.

Exit: the research record is sufficient to choose supported adoption,
experimental prototype, or rejection; the resulting `v0.4.0` PRD makes no
claim based only on kernel version, feature presence, or aspiration.

## Deferred backlog

- multiple concurrent gateways, explicit sharding, and cross-gateway failover
  (#31);
- general backup, restore, and disaster-recovery expansion (#32);
- product-wide metrics, alerts, and dashboards beyond eBPF diagnostics (#33);
- Loadstone lease-consumption certification;
- additional VPN engines and providers;
- cross-namespace reference grants and deeper multi-tenancy;
- Service-targeted lease handoff;
- kubectl plugin;
- Gateway API or CNI-native integration exploration;
- multi-cluster control plane.
