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

Exit: the signed OCI candidate replaces the PoC; qBitTorrent uses Waycloak for
protected egress and provider-port delivery during ordinary operation; the
gateway remains fail closed; and the verified final bundle is published.

## Phase 5 — provider and workload compatibility (`v0.3.0`)

- [ ] Complete sustained real-provider qBitTorrent ingress, advertisement,
  DHT, renewal, and actual rotation certification using the existing gated
  harness.
- [ ] Validate Bitmagnet and Loadstone consumption of the neutral lease
  contract.
- [ ] Record additional provider/application compatibility and troubleshooting
  evidence from real deployments.

Exit: qBitTorrent survives provider renewal or rotation without Pod
replacement, and the additional reference workloads have documented neutral
or evidence-backed narrow integrations.

## Phase 6 — operational maturity (`v0.4.0`)

- [ ] Add an observed admission release/generation gate that prevents mixed
  injected agent identities during zero-unavailable webhook upgrades (#55).
- [ ] Recover existing protected Pod UIDs automatically after a singleton
  gateway endpoint replacement while remaining fail closed (#61).
- [ ] Multiple named gateways.
- [ ] Gateway sharding design and implementation.
- [ ] Upgrade, rollback, backup, and disaster-recovery tests.
- [ ] Optional metrics, alerts, and dashboards.
- [ ] Performance/resource benchmarks.
- [ ] Compatibility matrix across supported Kubernetes/CNI combinations.

## Deferred backlog

- additional VPN engines and providers;
- cross-namespace reference grants and deeper multi-tenancy;
- Service-targeted lease handoff;
- kubectl plugin;
- eBPF data plane evaluation;
- Gateway API or CNI-native integration exploration;
- multi-cluster control plane.
