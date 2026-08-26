# Implementation roadmap

Each phase ends with observable acceptance criteria. A fresh implementation agent should take the first unchecked vertical slice, not build all packages speculatively.

> **Replacement plan (2026-07-26):** the completed phases below document the
> alpha implementation. New work follows the clean-break dependency graph in
> [stable-product-plan.md](stable-product-plan.md), beginning with the CNI
> creation-time feasibility proof. No backward-compatibility or translation
> work is planned. Do not extend the annotation/sidecar architecture except to
> keep the currently installed release fail closed during replacement.

**Release-candidate checkpoint (2026-08-22):** `v0.1.0-rc.26` preserves the
frozen `networking.waycloak.io/v1beta1` contract and packages the complete
product for signed direct consumption, Helm OCI, and KCL OCI. RC.25 completed
72.0002 hours on the exact local-cluster qBittorrent canary, but the evidence
found an unchanged-state provider-renewal/status feedback loop, hundreds of
adapter listener-probe timeouts, recurrent fail-closed DNS observations, three
lease handoffs, and a harness serialization gap during absent-provider states.
RC.26 honors the provider renewal schedule, serializes the same strict DNS
checks, and retains failed lease condition evidence. It changes no public API
schema or configuration contract. RC.26 remains a release candidate, not
closure of #123, #140, or #141 and not stable graduation. Exact publication,
GitOps deployment, live qBittorrent validation, and a fresh minimum 72-hour
local-cluster soak are required.

**Dependency-backed stabilization (2026-08-26):** ADR 0044 keeps the frozen
API and fail-closed behavior while delegating general protocol machinery to
qualified maintained dependencies. Work proceeds in this order:

- [ ] #242 establish exact dependency freshness, maintenance, security and
  lightweight-runtime gates.
- [ ] #245 separate workload-adapter Apply, Observe and renewal acknowledgement
  semantics so unchanged renewal is a no-op.
- [ ] #243 replace custom provider acquisition/renewal with Gluetun's native
  authenticated port-forward capability.
- [ ] #244 qualify a pinned CoreDNS gateway sidecar and retain Waycloak's strict
  semantic DNS readiness/no-fallback probe.
- [ ] #246 add bounded OpenTelemetry-first signals with no-op default and
  optional OTLP/Prometheus-compatible export.

Each slice is independently reversible and must pass exact artifact, packet,
DNS leak, adapter, GitOps qBittorrent, resource-budget and privacy gates. The
work does not authorize a new node, replacement CNI, service mesh, mandatory
collector, or weakened readiness. The successor release starts a new 72-hour
local-cluster graduation epoch.

**RC.18 publication correction (2026-08-17):** RC.17 passed exact main CI and
its CLI publication verifier, but its runtime uploader lost a GitHub release
creation race after all runtime artifacts and attestations had been produced.
Its replay then correctly refused to overwrite the immutable KCL version.
RC.17 is not deployable as a complete runtime release. RC.18 binds both
publishers to the explicit immutable tag name; it must pass both clean-download
verifiers before replacing RC.16 in the local-cluster qBittorrent canary.

**RC.19 resilient publication (2026-08-17):** RC.18 passed main CI and its CLI
publication verifier, and produced every runtime artifact and attestation, but
GitHub returned HTTP 503 for all final uploader retries. Its immutable KCL
version prevents an unsafe replay. RC.19 uses an exact-tag, concurrent-creator
aware, bounded-backoff `gh` transaction for the shared release. Both public
artifact verifiers remain mandatory before GitOps deployment.

**Extension-contract stabilization (2026-08-13):** ADR 0043 makes Gluetun the
VPN-engine integration boundary, contains Proton NAT-PMP behind a
Gluetun-selected port-forward capability, and makes application adapters an
explicit last resort. The frozen `v1beta1` resources do not change. The generic
runtime and installer no longer expose Proton- or qBittorrent-shaped activation
flags; unsupported capability/configuration pairs fail before gateway mutation.

- [x] #124 chained-CNI creation-time feasibility: merged in PR #143 with exact
  Kind, k3d and authorized homelab evidence.
- [x] #125 node-agent threat model and authenticated local protocol: merged in
  PR #144 with exact CI and authorized homelab evidence.
- [x] #126 alpha removal inventory and unknown-artifact audit: merged in PR #145
  with exact CI and review evidence.
- [x] #127 replacement API ownership and lifecycle freeze: merged in PR #146
  with exact CI and review evidence.
- [x] #128 replacement CRDs, generated APIs, and RBAC: merged in PR #147 after
  fresh-install discovery, schema, admission, RBAC, envtest, reproducibility,
  and review gates passed.
- [x] #129 VPNEgressRoute and explicit Pod enrollment: merged in PR #148 after
  exact label/UID, status, fail-closed lifecycle, admission, GitOps ordering,
  Kind, k3d, envtest, and reproducibility gates passed.
- [x] #130 cross-namespace parent consent and reference privacy: merged in PR
  #149 after uncached authorization, non-disclosure, revocation, dependency
  fan-out, envtest, Kind, and k3d gates passed.
- [x] #131 common conditions, per-parent status, and field ownership: merged in
  PR #150 after resource-scoped vocabularies, current generations, strict live
  readiness, SSA conflicts, concurrent convergence, no-op suppression, and
  transition-time stability passed.
- [x] #132 UID-bound binding allocation: merged in PR #151 after atomic
  reservation, concurrent allocation, restart, stale identity, exact
  withdrawal, quarantine, envtest, Kind and k3d gates passed.
- [x] #133 privileged node agent: completed in PR #152 after credential-free
  binding projection, agent-owned prepare/check/withdraw, drift/restart and
  reconfiguration reconciliation, exact-ServiceAccount observation relay,
  controller-loss lockdown, digest-pinned packaging, and privileged packet,
  Kind/kindnet, and k3d/Flannel gates passed.
- [x] #134 VPNGatewayClass and capabilities: completed in PR #153 after exact
  controller/release claim, class/gateway status, unsupported-feature and
  reference rejection, credential redaction, verified-manifest default-class
  rendering, envtest, Kind, k3d, race, and generated-artifact gates passed.
- [x] #135 alpha runtime removal: completed in PR #154 after replacement-only
  dependency/audit proof and exact-head unit, race, envtest, generated,
  reproducibility, security, Kind/kindnet, k3d/Flannel, and packet gates passed.
- [x] #136 minimal admission and CNI-capable scheduling: PR #155 proved the
  behavior, and PR #208 completed the pre-beta clean break from the externally
  visible `core-ready` preview label to the sole protected `cni-ready`
  identity. No alias or dual acceptance exists. Exact-head Linux verifier,
  admission, Kind/kindnet, k3d/Flannel, Gluetun, turnkey, and datastore-recovery
  step evidence passed again.
- [ ] #137 Service-backed SingleActive PortForwardLease: API, controller,
  privileged handoff tests, and the signed amd64/arm64 runtime images are
  implemented. The default-disabled chart/runtime deployment boundary now
  requires an exact runtime image, named mTLS Secrets, explicit gateway feature
  intent, and an exact gateway-owned Service while preserving a port-forward-free
  baseline render. The adapter image remains exact in its operator-authored trust
  record rather than becoming unused chart configuration. The next slice adds
  a confirmation-gated CLI transaction bound to a distinct exact release,
  complete release artifact inventory, immutable mTLS Secret UID/public
  digests, and exact controller SPIFFE identity. PR #205 initially coupled this
  to a separate candidate profile; the corrective slice keeps one Waycloak
  release and represents port forwarding only as an optional class capability.
  Disposable Kind covers wrong
  confirmation, Secret replacement, re-planning, immutable class replacement,
  and preservation of a two-container baseline gateway. Next publish that slice and
  deploy the operator-owned adapter through the homelab GitOps canary.
  Keep the capability unadvertised until the real-provider
  rolling-replacement test proves drain, successor handoff, return-path
  symmetry, zero wrong-Pod delivery, and zero ordinary-egress fallback.
- [x] #138: turnkey CLI and runtime installation are certified independently
  from the optional port-forward capability. Signed exact-artifact clean-install
  and credentialed provider evidence pass as two exact gates. The
  disposable Kind/local-OCI gate now exercises preflight,
  plan, refusal without exact confirmation, full apply, CNI receipt/chain,
  authenticated node capability, release identity, doctor, and a
  confirmation-gated disruptive smoke run through a kernel WireGuard fixture.
  The smoke run proves same-observer distinct source identity, gateway-loss
  startup denial, recovery, and owned-object cleanup without treating the
  fixture as a supported provider. RC.11's clean-cluster job completed in
  14m46s, and the same signed release identity passed the separate live
  Proton/OpenVPN qBittorrent GitOps canary. Release
  manifests are bound to a recomputed canonical
  version/chart/image/profile identity and reject hidden extra artifacts. The
  mandatory CNI installer image now has a repeat-built multi-platform OCI gate,
  and publisher tooling deterministically assembles the complete exact Core
  inventory without discovering or substituting an artifact. The node bootstrap
  path isolates attachment records from the install receipt and runs only the
  privileged infrastructure agent in the host network namespace, avoiding a
  circular dependency on its own CNI authority without exempting applications.
  Clean installs also wait for a controller-only Helm revision before activating
  the exact CNI/node-agent runtime; existing releases go directly to the full
  revision and never withdraw their installed deny path. The CLI tag workflow
  publishes prerelease suffixes as GitHub prereleases and uses a separate runner
  to redownload and verify the exact asset inventory, checksums, keyless
  workflow identity, issuer, source ref/commit, hosted-runner provenance, and
  SPDX signature. The `v0.0.0-turnkey.1` prerelease passed that exact publish
  and independent verification flow from main commit `21ffebea` in release run
  `30360505871`. The Waycloak release workflow publishes all six first-party
  multi-platform binaries, a vulnerability-gated Gluetun derivative,
  a tag-versioned chart, per-artifact SPDX, keyless OCI signatures and
  attestations, hosted-runner provenance, and the canonical eight-image manifest;
  its independent job verifies the downloaded and registry-resolved identities.
  The Gluetun artifact preserves the exact upstream commit/image identity, MIT
  license, and dependency-only patch instead of accepting reachable fixed
  vulnerabilities in the current upstream image. This remains implementation-only until an
  exact `vMAJOR.MINOR.PATCH-beta.NUMBER` tag passes. The real-provider journey
  is still required before closure.
  Creation-time Pod UID and node assignment checks use direct API-server reads,
  never the node agent's eventually consistent reconciliation cache.
  Deployment plans now also bind a canonical hashed cluster observation and
  re-run it immediately before mutation. Mixed-architecture clusters require
  an explicit reviewed row, and the CNI installer/node agent run only on that
  architecture so an unproved node cannot publish Core capability. Doctor now
  derives the expected capability set from those two live selectors, requires
  them to agree, reports other architecture rows as `NotSelected`, and still
  fails for any selected node without a current authenticated capability.
- [x] #139: exact read-only inventory and confirmation-gated CR/CRD purge pass
  the repeated disposable Kind purge-to-fresh-install sequence. The authorized
  homelab alpha drill independently proved runtime quiescence, separate
  uninstall/purge, clean signed replacement, zero fallback, and fresh state
  reacquisition. Its 301-second uninstall is a documented bounded maintenance
  limitation, not a reason to repeat destructive production work.
- [ ] #140: `v0.0.0-core.7` passed exact publication and independent hosted
  verification; Core.8 through Core.10 also passed their exact publication and
  registry-native verification gates. The live clean-break amd64 row reached
  Core.10 through exact lifecycle plans and stayed unavailable during gateway
  startup defects. Its next exact candidate grants only the additional Gluetun
  privilege-drop capability and separates the overlay DNS listener on 1053 from
  Gluetun's loopback port 53. Core.11 proved those corrections live, then exposed
  the bounded restart, Kubernetes DNS-search, and current Gluetun control-auth
  contracts. Core.12 implemented those contracts, passed exact hosted and local
  verification for both OCI architectures, and reached a 2/2 Ready live amd64
  gateway with zero restarts through the 10.5-minute checkpoint. Homelab GitOps
  promotion preserved the controller and gateway Pod identities but exposed
  stable API-server defaults missing from the rendered MutatingAdmissionPolicy,
  leaving Argo CD OutOfSync. Core.13 renders those defaults explicitly, passed
  hosted and independent exact-artifact verification, and converged Argo CD to
  Healthy/Synced without replacing the controller or gateway or restarting
  either container. Core.17 to Core.18 then completed the signed, journal-bound
  two-revision homelab transition: the immutable class received a new UID,
  observation Secret UIDs were preserved, and 78 six-minute qBittorrent samples
  observed zero ordinary-egress fallback, zero workload Pod replacement, and
  zero workload restart. Three protected egress probes were denied and one
  external HTTP probe failed during the bounded handoff. That live row also
  exposed a lifecycle defect: the generated singleton gateway StatefulSet used
  Kubernetes' default `RollingUpdate`, automatically activating the new gateway
  template instead of waiting for the required operator action. Core.19 makes
  `OnDelete` explicit and corrects existing replacement StatefulSets before
  future template changes. Its signed Core.18-to-Core.19 homelab transaction
  left the exact gateway Pod UID and Core.18 images unchanged while staging the
  Core.19 template. The separate explicit activation produced a new exact
  Core.19 gateway UID and 75/75 qBittorrent HTTP successes with three
  fail-closed protected denials, zero ordinary-egress matches, zero workload
  replacement, and zero workload restart. The current-generation class,
  gateway, route, and binding recovered Ready, the node receipt identified the
  target manifest, and Argo CD converged Healthy/Synced. Corrected forward
  transition and activation evidence are complete. Core.20 now binds the
  generated singleton template to the signed release version and manifest
  digest and proves forward/rollback staging even with unchanged gateway binary
  digests while the live Pod retains its source identity. Its independently
  verified homelab Core.19-to-Core.20-to-Core.19-to-Core.20 sequence preserved
  the exact live gateway through each signed transaction, required explicit
  activation, retained one qBittorrent Pod with zero restarts, and recorded zero
  ordinary-egress matches. The final Core.20 activation recovered after three
  fail-closed denials; the Core.19 rollback activation required 50 denied probes
  before later partial recovery and remains part of #116. This is rollout
  evidence, not compatibility support. Interrupted lifecycle, beta CRD
  lifecycle, uninstall/purge, sustained soak, and remaining support rows
  are still required.
- [x] #32: portable logical backup/restore (#174), exact source-bound forward/
  rollback (#175), journal-bound staged interruption recovery (#176),
  observation-certificate rotation (#177), and bounded pending/corrupt Helm
  repair (#178) are merged with exact hosted evidence. PR #179 pins the first
  distribution row to single-server K3s `v1.36.1+k3s1` embedded etcd. Exact-head
  run `31361748255` passes cold CRI-quiesced snapshot/reset recovery with
  coherent Pod UID return, fresh sandbox identity, stale-ready withdrawal,
  exact first chained-CNI restoration, durable deny state, zero direct packets,
  and primary-CNI controls. Warm service-only reset is explicitly unsupported
  after it allowed an enrolled application to resume with direct egress. This
  completes the declared recovery dependency. A destructive homelab restore or
  an additional cluster node is not required; any other datastore topology is
  a separately declared future support row.
- [x] #33: bounded aggregate controller metrics, classified fail-closed versus
  availability alerts, a plain Prometheus scrape fragment, and an optional
  Grafana dashboard passed all eight jobs in exact-head run `31366217996`.
  Turnkey Kind proved live gateway/tunnel/DNS and missing-route protection state,
  application non-start, and privacy-canary absence from the installed scrape.
- [ ] #141: the live `amd64` K3s/Flannel Proton/OpenVPN row, exact-artifact
  lifecycle, destructive reinstall, DR, and beta evidence are recorded. RC.13
  makes that deliberately narrow row machine-readable in the signed manifest.
  RC.13 also stages each handoff generation before external effects after the
  RC.12 live upgrade exposed an ahead-of-status adapter generation. Its live
  GitOps replacement retained the qBittorrent Pod and lease identities while a
  watch observed the durable `Selecting` generation before automatic
  activation. The RC.13 local-cluster epoch was stopped after raw condition
  history exposed brief controller-observation withdrawals and safe lease
  generation churn that the one-minute sampler missed. RC.14 bounds and
  classifies that observation path without retrying a valid not-ready response.
  Its exact GitOps upgrade then exposed a stale status force-apply that could
  regress handoff generation across controller replacement while the runtime
  and persistent adapter remained ahead. RC.15 makes lease-status writes
  resource-version checked, so the stale reconcile conflicts instead. Publish
  and deploy the successor exact artifact, then complete a fresh minimum 72
  unchanged-artifact hours before v1 graduation. ARM and other platforms are
  future rows, not hidden blockers.

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

### v0.3.2 reliability patch

- [x] Replace disabled HTTP keep-alives with a bounded Gluetun loopback
  transport and independently verify only transient EOF/reset failures.
- [x] Keep HTTP error statuses, timeouts, cancellation, and repeated transport
  failures authoritative so readiness still fails closed.
- [x] Emit structured, transition-only engine, gateway-health, and retained
  desired-state diagnostics without logging credentials or public endpoints.
- [x] Preserve the last valid desired-state generation across a transient
  projected-file absence.
- [x] Remove avoidable resource writes and preserve concurrent metadata during
  gateway status patches.
- [x] Cover sustained intermittent engine failures and concurrent controller
  updates in automated tests.
- [x] Reject stale PortForwardLease status writes across controller handover so
  an already-applied handoff generation cannot be overwritten by an older
  reconcile.
- [x] Publish only completed gateway DNS observations: retain the last
  successful result while the next probe is in flight, but immediately
  withdraw readiness and reinstall deny rules when that probe actually fails.
- [x] Start lease reconciliation from an authoritative API-server read and
  emit endpoint-safe, transition-only diagnostics for every initiated handoff
  so informer lag and each drain predicate can be distinguished live.
- [ ] Complete the minimum 72-hour unchanged-artifact local-cluster soak for
  #116/#141 with qBittorrent as the sole application canary. Record outage
  counts and durations, DNS state, lease renewal/withdrawal/recovery, listener
  and DHT state, packet evidence, restarts, identity changes, and bounded
  resource writes. Do not add nodes or activate Bitmagnet. The Aug 11–17
  cross-release history and the invalidated RC.13 epoch are lifecycle evidence
  but do not replace this epoch.

### v0.3.3 controller correctness patch

- [x] Compute the final `PortForwardLease` delivery conditions once per
  reconciliation instead of introducing an unpersisted pessimistic transition.
- [x] Preserve `lastTransitionTime` while condition status remains unchanged,
  including unchanged polls and expiry-only provider renewals.
- [x] Suppress no-op status writes through semantic status equality.
- [x] Publish and deploy the digest-pinned release, then verify that stable
  `Ready` and `Delivered` conditions retain their transition timestamps.

### v0.3.4 sidecar Pod-sandbox recovery patch (#121)

- [x] Diagnose the fail-closed qBitTorrent bootstrap after CRI Pod-sandbox
  recreation as a stale prepared VXLAN endpoint plus a newer projected
  allocation, rather than weakening startup gates.
- [x] Make startup verification reinstall lockdown and reconcile the latest
  complete allocation before it probes observed gateway readiness.
- [x] Resolve replacement gateway underlay routes only after installing the
  current endpoint-specific main-table rule.
- [x] Add unit and privileged packet coverage for deny-first failure and
  stale-to-current endpoint reconciliation.
- [x] Add packaged-image lifecycle coverage that stops the exact CRI Pod
  sandbox, proves ordinary egress remains closed, and requires same-UID
  recovery across a CNI underlay-address rollover.
- [ ] Pass the Linux race/envtest and packaged-image release gates, publish the
  immutable patch release, deploy it through the normal homelab pipeline, and
  verify workload plus lease withdrawal/recovery on live provider traffic.

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
- [x] Complete primary-source research for attachment, persistence, replacement,
  privilege, verifier, portability, CNI ownership, and sidecarless models (#65).
- [x] Resolve the leading Pod-cgroup identity/lifecycle and containerd CNI
  creation-time handoff with
  disposable, non-production probes (#65).
- [x] Test the minimum deny-only cgroup prototype needed to decide feasibility;
  defer host-veth tc/TCX because E2 remains viable (#65).
- [x] Establish cross-architecture default-backend and real-provider scaling
  baselines, and make equivalent preview comparison a release gate (#34).
- [x] Publish the architecture comparison, threat-model delta, support boundary,
  recommendation, and rejected alternatives (#65, #34).
- [x] Derive the `v0.4.0` PRD, release cutoff, follow-up ADR direction,
  conformance requirements, and ordered GitHub issue graph.

Exit: the research record is sufficient to choose supported adoption,
experimental prototype, or rejection; the resulting `v0.4.0` PRD makes no
claim based only on kernel version, feature presence, or aspiration.

## Phase 7 — v0.4.0 eBPF node-data-plane developer preview

Implement [the v0.4.0 release PRD](../product/release-scope-v0.4.md) in strict
dependency order. The sidecar backend remains the supported default throughout.

- [ ] Freeze the preview API, node capability/status contract, threat-model
  amendment, and backend-neutral conformance matrix (#107).
- [ ] Build the disposable containerd CNI handoff probe and prove exact Pod UID,
  netns, cgroup-parent, ordering, idempotence, and rollback on amd64 and arm64
  (#108).
- [ ] Implement UID/generation-owned cgroup programs, pins, atomic updates,
  adoption, severed-link handling, reboot recovery, and bounded garbage
  collection in the node agent (#109).
- [ ] Prototype node ownership of VXLAN, routes, DNS NAT, verification, and
  drift repair; accept the privilege boundary explicitly before removing the
  Pod networking agent (#110).
- [ ] Implement explicit admission selection, prepared-node scheduling, stable
  unsupported reasons, runtime capability-loss behavior, and no fallback (#111).
- [ ] Package immutable CNI/node-agent artifacts with atomic installation,
  upgrade, rollback, and safe uninstall (#112).
- [ ] Run equivalent default/tuned-default/preview performance and component
  measurements at 1, 10, 50, and stress counts on amd64 and arm64 (#113).
- [ ] Pass the complete declared-feature conformance suite, default-mode
  regression, signed-artifact policy, and mixed-mode homelab acceptance (#114).

Exit: the exact signed `v0.4.0` is deployed in the homelab with sidecar and
preview workloads coexisting; the preview has a documented narrow support
boundary, measured value, and zero observed direct-egress packets through every
required lifecycle transition.

## Deferred backlog

- multiple concurrent gateways, explicit sharding, and cross-gateway failover
  (#31);
- general backup, restore, and disaster-recovery expansion (#32);
- additional provider-specific or high-cardinality telemetry beyond the stable
  aggregate #33 contract;
- Loadstone lease-consumption certification;
- additional VPN engines and providers;
- any future upstream ReferenceGrant integration and deeper multi-tenancy;
- Service-targeted lease handoff;
- kubectl plugin;
- Gateway API or CNI-native integration exploration;
- multi-cluster control plane.
