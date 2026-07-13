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

Next vertical slice: implement the minimal agent/fake-gateway contract needed to install owned deny state before application startup and packet-test that gateway or agent loss cannot fall back to ordinary egress. Do not add Gluetun in this slice.

- [ ] Build minimal non-root-where-possible agent image.
- [ ] Install owned nftables policy before application startup.
- [ ] Establish and monitor VXLAN to a test gateway.
- [ ] Implement route and firewall drift repair.
- [ ] Implement cluster-local policy modes.
- [ ] Implement gateway-routed DNS.
- [ ] Add preflight diagnostics.

Exit: forced agent/gateway/tunnel failures produce no direct external packets and service DNS works according to policy.

## Phase 3 — Gluetun gateway (`v0.1.0`)

- [ ] Reconcile gateway StatefulSet, Service, configuration, RBAC, and disruption controls.
- [ ] Integrate pinned Gluetun engine.
- [ ] Implement tunnel and public-egress health observations.
- [ ] Apply membership incrementally without tunnel restart.
- [ ] Package a signed Helm OCI chart and images.
- [ ] Publish install, security-exception, troubleshooting, and uninstall guides.

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
