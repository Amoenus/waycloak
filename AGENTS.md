# AGENTS.md

## Mission

Implement Waycloak as a generalized, Kubernetes-native private-egress product. Preserve the invariant that an opted-in workload never silently falls back to ordinary internet egress.

## Read first

Before implementation, read these documents in order:

1. `PROJECT_STATUS.md`
2. `docs/product/PRD.md`
3. `docs/product/developer-experience.md`
4. `docs/architecture/architecture.md`
5. `docs/architecture/networking.md`
6. `docs/security/threat-model.md`
7. `docs/api/api-contract.md`
8. `docs/implementation/roadmap.md`
9. `docs/testing/test-strategy.md`
10. the relevant ADRs under `docs/decisions/`

## Non-negotiable invariants

- Opt-in is explicit and visible on the workload Pod template.
- Opted-in traffic fails closed during startup, tunnel loss, agent loss, reconfiguration, and gateway replacement.
- VPN credentials are referenced by the gateway and are never duplicated into protected workloads.
- Plain Kubernetes is the primary API. Helm is the primary installation surface. KCL is optional.
- Applications do not have to share a Pod with the VPN engine.
- Membership and port leases have stable identities; alphabetical renumbering is forbidden.
- `Ready` must describe observed data-plane health, not only successful registration.
- Application containers receive no additional Linux capabilities from Waycloak.
- Every externally visible behavior needs an end-to-end acceptance test.

## Implementation discipline

- Prefer Go for the controller, webhook, gateway manager, and routing agent.
- Prefer netlink/nftables APIs over shelling out. A shell-script prototype is acceptable only behind tests and a migration issue.
- Keep provider-specific behavior behind an interface. The first driver may target Gluetun and Proton/OpenVPN.
- Avoid a hard runtime dependency on Argo CD, Crossplane, ESO, KCL, cert-manager, or Prometheus Operator.
- Never claim anonymity. Describe the guarantee as selected, fail-closed VPN egress within the documented threat model.
- Do not publish mutable `latest` references in examples or releases.
- Preserve license notices when adapting Apache-2.0 upstream work.

## Required verification before merging

- formatting, static analysis, unit tests, and race tests;
- envtest/controller reconciliation tests;
- Kind or k3d end-to-end tests for annotated and unannotated workloads;
- tunnel-loss and DNS-leak tests;
- generated CRDs and Helm templates are reproducible;
- image and chart vulnerability policy passes;
- no credentials, private endpoints, or homelab-specific values are committed.

## Current task selection

Work in roadmap order. Do not begin multi-gateway sharding, a UI, or additional providers before the `v0.1.0` fail-closed proof works. Update `PROJECT_STATUS.md` and the roadmap when a milestone changes state.
