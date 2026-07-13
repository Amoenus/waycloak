# Implementation roadmap

Each phase ends with observable acceptance criteria. A fresh implementation agent should take the first unchecked vertical slice, not build all packages speculatively.

## Phase 0 — repository and design baseline

- [x] Product PRD and developer experience.
- [x] Architecture, networking, and threat model.
- [x] Proposed API contract.
- [x] Test and release requirements.
- [x] Homelab prototype provenance.
- [ ] Resolve remaining open API questions through ADRs.
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

The functional gateway path is complete. The controller-owned singleton StatefulSet, headless Service, read-only engine configuration, gateway-manager runtime, pinned Gluetun adapter, stable membership publication, native gateway VXLAN, deny-first forwarding/NAT, and split-DNS proxy are implemented. This includes digest-only images, engine-only credential mounting, token isolation, owner cleanup, generated RBAC, typed tunnel/DNS/public-IP observations, observation-driven component status, exact cluster-DNS firewall/routing exceptions, and manager-owned VXLAN source authorization. Fixture tests remain explicitly non-VPN. A gated real-provider k3s acceptance now proves distinct protected public egress through the production path, Kubernetes DNS containment, UID-gated startup, credential isolation, and fail-closed behavior after abrupt gateway deletion without exposing Secret or public-IP values. Next vertical slice: disruption controls and the minimal Helm/release surface for the proven path.

- [x] Reconcile gateway StatefulSet, Service, configuration, and RBAC.
- [x] Add gateway and controller/webhook disruption controls without cloning the singleton tunnel.
- [x] Integrate pinned Gluetun engine and prove the production protected-Pod path against a real provider.
- [x] Implement tunnel and public-egress health observations.
- [x] Apply membership incrementally without tunnel restart.
- [x] Add a deterministic Helm chart and multi-architecture controller image build.
- [x] Implement the pinned, keyless image/chart publication pipeline, SBOM/provenance gates, and signed release-manifest tooling.
- [ ] Execute the protected tag workflow and verify the published OCI artifacts.
- [x] Publish install, security-exception, troubleshooting, and uninstall guides.
- [x] Prove zero-unavailable Helm upgrade/rollback, two-phase webhook certificate rotation, and operator-activated singleton gateway rollouts.

Exit: e2e acceptance proves annotated VPN IP, unannotated normal IP, fail-closed outage, DNS containment, and credential isolation on Kind and k3s/k3d.

## Phase 4 — port forwarding (`v0.2.0`)

- [ ] Define provider capability interface and `PortForwardLease` API.
- [ ] Implement Proton NAT-PMP driver through the tunnel.
- [ ] Persist stable lease identities and generations.
- [ ] Reconcile TCP/UDP DNAT atomically.
- [ ] Deliver neutral lease records to workloads.
- [ ] Publish qBitTorrent adapter/example.
- [ ] Validate Bitmagnet and Loadstone consumption.

Exit: qBitTorrent receives inbound TCP/UDP through a provider lease, reports healthy DHT, and survives lease renewal during a sustained test.

## Phase 5 — operational maturity (`v0.3.0`)

- [ ] Multiple named gateways.
- [ ] Gateway sharding design and implementation.
- [ ] Upgrade, rollback, backup, and disaster-recovery tests.
- [ ] Optional metrics, alerts, and dashboards.
- [ ] Performance/resource benchmarks.
- [ ] Compatibility matrix across supported Kubernetes/CNI combinations.
- [ ] Optional KCL OCI module.

## Deferred backlog

- additional VPN engines and providers;
- cross-namespace reference grants and deeper multi-tenancy;
- Service-targeted lease handoff;
- kubectl plugin;
- eBPF data plane evaluation;
- Gateway API or CNI-native integration exploration;
- multi-cluster control plane.
