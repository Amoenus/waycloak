# Project status

## Stable publication security refresh

The first `v1.0.0` runtime publication attempt at signed source
`3bee8a32` stopped at the mandatory vulnerability gate after publishing only
intermediate immutable images. The CLI publication and verifier passed, but no
complete runtime release or canonical manifest was published. Trivy's current
database identified critical CVE-2026-56854 in the official CoreDNS v1.14.7
binary's `golang.org/x/crypto` v0.54.0; v0.55.0 contains the fix. This is a
substantive release blocker and is not waived by the RC.42 soak decision.

CoreDNS v1.14.7 remains the latest maintained stable upstream release. The
successor keeps its exact source commit and unchanged DNS implementation, runs
the upstream suite after applying the maintained `x/crypto` v0.55.0 and
`x/mod` v0.40.0 dependency updates, and requires two independent builds to
produce byte-identical amd64 and arm64 binaries. The release publishes a signed
Waycloak CoreDNS derivative
with the exact upstream reference, dependency patch, binary checksums, license,
SBOM, vulnerability scan, signature, and hosted provenance. This adds no DNS
protocol code or alternate resolver: CoreDNS still owns DNS serving and
Waycloak still owns strict semantic readiness and fail-closed withdrawal.

## RC.42 executive certification disposition

`v0.1.0-rc.42` is the accepted local-cluster release candidate at signed
source commit `41dfcf91679d365e5fdc4eadb9ff9a1511efec0d`, canonical manifest
`sha256:96b283312317ffb6216224ff2a60c2bb322ba76398c360d481e6d90e4162c188`,
and homelab GitOps revision
`10b463b3ced056742ce9eea2ebf4e2b0f8984fd2`. Publication, independent exact
artifact verification, the confirmation-bound transition, immutable adapter
trust rotation, repeated gateway-engine recovery, transient and persistent DNS
failure behavior, DNS load, tunnel fail-closed recovery, qBittorrent
TCP/UDP/listener/DHT/tracker behavior, provider and gateway rotation,
rollback/repair, resource, privacy, metrics, and packet gates passed.

The unchanged-artifact RC.42 epoch ran from
`2026-08-31T04:39:47.0660014Z` until the executive decision snapshot at
`2026-09-01T17:34:59.7254492Z`, for 36.920183 hours. The project owner accepted
that evidence as stable enough and waived the remaining 35.079817 hours for
this candidate. This is an explicit one-time executive exception: it does not
claim that the documented 72-hour duration was observed and does not weaken
the normal qualification policy for later candidates.

The final incomplete-duration audit remained healthy with 2,177 canonical
samples, 2,147 qBittorrent samples, 213 successful external TCP checks and zero
failures, 441 metrics samples, and zero non-True gateway, lease, binding, or
binding-heartbeat observations. All five collectors were stopped after their
identity was verified. The credential-contained terminal proof confirmed the
acknowledged listener, DHT, cluster and external DNS, tunnel egress, external
TCP reachability, and bidirectional TCP and UDP packet traffic. Earlier failed
RC.30-RC.41 epochs and the three RC.42 collector/precondition failures remain
preserved and are not merged into the accepted epoch.

The signed evidence disposition is stored outside the repository at
`waycloak-rc42-soak-20260831T043946Z-executive-waiver.json` with SHA-256
`561e2313ff20d8d7f0ea38c63f3a56c16f44b26c4bcb7dfb13d45e25b2404bbe`.
The terminal certification record is
`waycloak-rc42-terminal-certification-proof.json` with SHA-256
`1eb37677f55a57c168219498d44876530a7094aefe02c3dea9f9c10c20d3e337`.
Neither record contains a public endpoint, credential, or provider port.

## OpenTelemetry implementation slice

Issue #246 is implemented and qualified in exact RC.42 release and live-cluster
evidence. OpenTelemetry Go v1.46.0 is the single
internal operational signal model for gateway DNS/engine readiness, provider
mapping refresh, rules and delivery, acknowledgement, qBittorrent adapter
Apply/Observe, withdrawal, and recovery. The default creates no SDK, exporter,
listener, worker, or collector dependency.

When enabled, reconciliation only attempts a non-blocking write to a bounded
256-event queue. Saturation drops telemetry and is counted; export failures are
counted. OTLP work has short export budgets, and focused traces share the
bounded event queue and batch. An optional
collector can translate the same OTLP instruments to Prometheus output. Kubernetes
Conditions, Events, semantic probes, and packet observations remain
authoritative and exporter failure cannot affect readiness.

The signal schema accepts only fixed component, operation, result, phase,
transport, DNS type, and failure-class enums. It has no representation for
credentials, endpoints, addresses, ports, UIDs, names, digests, arbitrary
domains, torrents, or error messages. Host benchmarks measured a disabled call
at 0.28-0.29 ns/op and a saturated enabled queue at 36-40 ns/op, both with zero
allocations. Stripped Linux/amd64 binary growth is 152-200 KiB (0.76-2.00%)
across the three instrumented components. Exact image/RSS, collector-loss,
live failure localization, release, GitOps, and accepted-soak evidence passed
with RC.42.

## CoreDNS gateway-sidecar implementation slice

Issue #244 is implemented and qualified in exact RC.42 release and live-cluster
evidence. The 391-line custom DNS server
has been removed from the gateway agent. A digest-only CoreDNS v1.14.7 sidecar
now owns split-DNS serving in the existing gateway Pod network namespace. It
runs as non-root with a read-only filesystem, bounded resources and concurrency,
and only the official executable's required `NET_BIND_SERVICE` capability after
dropping all others. It binds Waycloak's unprivileged port 1053. A
fail-closed gateway-agent init invocation establishes the overlay before the
sidecar binds.

The cluster boundary is implementation-neutral: preflight supplies a generic
UDP/TCP port-53 endpoint and cluster suffix. The sidecar has no ServiceAccount
token, Kubernetes plugin, kubeconfig, or API dependency, so alternative cluster
DNS implementations remain supported when they satisfy that standard DNS
contract. External names have only the Gluetun loopback upstream. There is no
resolver fallback or permissive hysteresis.

Waycloak retains the readiness authority through exact response validation for
cluster A and external A/AAAA over both UDP and TCP, including EDNS0 and TCP
retry after truncation. One failed path immediately clears readiness and
withdraws forwarding. The official CoreDNS index and amd64/arm64 manifests are
pinned in dependency and signed-release inventories with SPDX and vulnerability
gates. Exact publication, homelab GitOps deployment, the live client matrix,
resource measurements, DNS leak/tunnel-loss/rotation/rollback evidence, and
accepted-soak evidence passed with RC.42.

## Gluetun-native port-forward implementation slice

Issue #243 is implemented and qualified in the exact derived RC.42 image and
live-cluster evidence. The custom Proton NAT-PMP client
has been removed. Gluetun v3.41.3 now owns provider mapping acquisition,
renewal, and release; Waycloak observes the single shared TCP/UDP mapping over
an API-key-authenticated loopback control route and retains stable lease/target
identity, translation, freshness, handoff, acknowledgement, status, atomic
rules, and fail-closed withdrawal.

The authenticated policy and key are generated by a short-lived init container
from the existing runtime TLS identity and stored in a memory-backed volume.
Only DNS status, public-IP observation, and port-forward observation GET routes
are authorized. Port-forward lifecycle settings are release-owned, so operator
configuration cannot start a competing renewal loop. The frozen `v1beta1`
`expiresAt` field records a bounded observation-validity deadline because
Gluetun exposes no provider TTL; it is not described as provider lease expiry.
The exact v3.41.3 source commit and upstream index digest are pinned, with
current dependency-only rebuilds and an Alpine package upgrade behind the
existing upstream-test, govulncheck, reproducibility, and Trivy gates. Exact
release, GitOps deployment, real-provider renewal/rotation/rollback, and
qBittorrent TCP/UDP evidence passed with RC.42.

RC.37 live qualification acquired and renewed otherwise healthy Proton
mappings across multiple gateway sessions, but independent TCP checks never
delivered a matching inbound packet to `tun0`. Direction-aware nftables
counters, packet capture, and Gluetun's upstream local-listener isolation
procedure excluded Waycloak DNAT, the workload adapter, and qBittorrent from
that failure boundary. The exact v3.41.3 binary was then found to embed Proton
server data timestamped 2025-11-18 with 1,600 records. The latest maintained
`gluetun-servers` v0.2.0 release is a GitHub-verified exact commit from
2026-08-06 with 2,096 same-schema Proton records. The successor imports only
that upstream dataset reproducibly, preserves its MIT license and exact
commit/checksums, and adds no provider protocol implementation or runtime
dependency. RC.37 remains rejected before soak; a new exact candidate must
repeat provider ingress and every pre-soak gate.

The first successor publication attempt, v0.1.0-rc.38, was rejected before
release completion because the vulnerability workflow treated the new
`gluetun-servers.ref` source-provenance asset as an OCI image reference. All
source CI, including the duplicate exact Gluetun/data build and derived-image
scan, passed. The release gate now enumerates only the eight runtime OCI
reference assets explicitly; source-provenance references remain independently
verified release inputs but cannot be passed to the image scanner. RC.38 is not
reused; the corrected exact source requires a new signed candidate.

RC.39 restored provider ingress and began an unchanged-artifact epoch, which is
preserved as invalid. A node-observation publication timeout caused correct
fail-closed withdrawal but unnecessarily advanced the unchanged backend
handoff identity. RC.40 published the identity-preservation correction at
source `a7843cef2bcaedf0bb4041de0c508b475ebb475f` with canonical manifest
`sha256:d63bbf06e3aa85004db620e5fd76afa9015657fbe625ad5111b5fa31892de62e`.
Its RC.39-to-RC.40 live transition stopped safely before Helm upgrade because
the successor node-agent required exact installed-CNI identity flags that the
source RC.39 chart did not render during the pre-transition hold. Supplying the
exact source version and manifest identity restored the held agent; the same
immutable plan then completed and the gateway, binding, mapping, delivery, and
listener acknowledgement recovered. RC.40 remains invalid for certification
because manual intervention was required. The corrective source makes
pre-transition quiescence add the absent source CNI identity, retain already
exact values, and reject duplicates or foreign values before mutation. It adds
no dependency or permissive behavior; this signed release-state transition is
Waycloak-owned logic over the existing qualified Kubernetes clients. A new
signed candidate, exact GitOps transition, repeated pre-soak gates, and fresh
minimum 72-hour unchanged-artifact epoch remain required.

RC.41 published that transition correction from signed source
`b72cf7c7f47144afbd37db0f0e2132c61586b2bd` with canonical manifest
`sha256:5ff7d338da37c0bb47190a9436b50bec71274f0ac82dacf43b093b5b54689f9a`.
The confirmation-bound RC.40-to-RC.41 transition, homelab GitOps revision
`6421ce69b4aea66f29b3d5aa17c7d3f65cdeeafb`, immutable adapter rotation,
DNS load, renewal, tunnel withdrawal/recovery, provider rotation, qBittorrent
listener/DHT checks, and packet-path checks passed. A second controlled
`vpn-engine` restart then exposed a separate handoff convergence defect before
soak. After generation 292 was fully withdrawn while the gateway was
unavailable, the controller persisted a selecting generation 293 although the
runtime had never installed it. The next unavailable-gateway reconcile tried
to withdraw that phantom successor from a runtime that still correctly
recorded drained generation 292, so the lease remained fail closed in
`ObservationUnavailable` with repeated generation conflicts. RC.41 is invalid
before soak. The correction defers successor identity and generation creation
until the gateway is observed Ready; recovery then durably selects the next
generation before invoking the runtime. This is Waycloak-owned handoff state,
so no third-party library is appropriate and no dependency, API field,
hysteresis, or permissive fallback is added. A new exact candidate must repeat
the pre-soak gates before a fresh minimum 72-hour unchanged-artifact epoch.

## Adapter apply/observe implementation slice

Issue #245 is implemented and qualified in exact RC.42 release and local
qBittorrent evidence. The stable v1 JSON contract is
unchanged. The qBittorrent implementation now separates application `Apply`,
listener `Observe`, and expiry-only renewal acknowledgement. A changed exact
identity is durably applied once, then acknowledged only after matching
preference state plus TCP and transaction-bound UDP/DHT listener observation.
Observation retries are bounded below the fail-closed freshness deadline, and
sustained loss remains unavailable.

Expiry-only renewal performs no login, preference write, reannounce, or
handoff change. Adapter-Pod replacement reloads durable state and reobserves
without reapplying. Withdrawal never uses cached acknowledgement. Permanent
stale/contradictory identities remain HTTP 409, while dial, authentication,
application API, and listener failures are HTTP 503 and retain typed
`conflict`/`unavailable` classification across the gateway-runtime hop. Focused
unit tests and Linux-target Staticcheck v0.8.1 pass. The ordered source slices
in issues #243, #244, #245, and #246 all passed exact RC.42 GitOps and
qBittorrent validation.

## Dependency governance slice

Issue #242 is implemented. Waycloak now carries a machine-readable inventory
covering every direct Go module, shipped base image, generated-client/build
tool, and the chart, KCL module, and nine exact release images. A deterministic
audit rejects inventory/go.mod drift, missing pins or qualification evidence,
expired maintenance or lag reviews, incomplete release coverage, and resource
budget regressions. CI also reports upstream drift without changing pins, and a
weekly scheduled workflow catches drift when the repository is otherwise idle.
The signed runtime release includes the byte-identical inventory in its
checksum/provenance set, and independent publication verification consumes and
revalidates it.

The 2026-08-31 refresh added the exact maintained `gluetun-servers` v0.2.0
runtime-data source after live qualification exposed stale embedded Proton
metadata. The 2026-08-29 refresh advanced Prometheus client_golang to v1.24.1, x/net to
v0.58.0, x/sys to v0.47.0, KCL CLI to v0.12.8, Trivy to v0.74.0, actionlint to
v1.7.12, Staticcheck to v0.8.1, crane to v0.22.0, Helm to v4.2.4, Kind to
v0.33.0, and the qualified SHA-pinned GitHub Actions. Linux-target compilation,
generated KCL comparison, workflow validation, unit tests, reproducible binary
size measurements, and the existing per-agent RSS evidence remain within the
published budget. The live local cluster was directly observed at
Kubernetes v1.36.1+k3s1, not 1.37, so Kubernetes v1.37 is an explicit #141
compatibility lag reviewed by 2026-11-27. All ordered #242-#246 source slices
are now implemented; exact release and live evidence are next.

RC.30 was the first corrective publication candidate for those slices. RC.29 was
published and exactly deployed, but live validation found that the
port-forward table's unmatched-tunnel rule dropped established replies for
ordinary protected-workload TCP egress. RC.30 preserves tracked tunnel return
traffic before the fail-closed unmatched-ingress drop. It delegates provider
mapping acquisition and renewal to Gluetun, separates adapter Apply and Observe,
uses a qualified private CoreDNS gateway sidecar while keeping Waycloak's strict
semantic probes, and adds bounded no-op-by-default OpenTelemetry signals. The
cluster DNS upstream remains implementation-agnostic behind its configured
Service address and cluster domain. The live cluster is still Kubernetes
v1.36.1+k3s1, so RC.30 does not claim a 1.37 certified row.

Both RC.30 soak epochs are invalid for stable graduation. The first retained
an isolated external TCP checker failure. The replacement epoch then recorded
repeated external TCP failures, strict DNS-readiness withdrawals, and handoff
generations through at least 246 without any Pod UID, restart, release, or
GitOps change. CoreDNS localized every observed upstream DNS timeout to
Gluetun's loopback resolver, while Gluetun recorded DNS-over-TLS timeout and TLS
connection failures during the largest burst. The simultaneous independent TCP
failures mean the deepest cause may include the selected VPN/provider path and
is not attributed to CoreDNS alone.

RC.31 carries the bounded DNS-path correction. It uses Gluetun's maintained
DNS implementation with qualified defaults of DNS-over-HTTPS and the
`cloudflare,google,quad9` resolver set while preserving explicit native Gluetun
DNS overrides. The 2026-08-29 refresh reconfirmed Gluetun v3.41.3, CoreDNS
v1.14.7, and `golang.org/x/net` v0.58.0 as the latest stable qualified
versions. No new DNS library, public API field, fallback resolver, readiness
hysteresis, or custom DNS serving code was added.

Signed `v0.1.0-rc.31` was published from exact commit
`12845402a7ee76e2b9925638cf987689e84a88f3` after main CI run `33274865219`,
runtime release run `33275624955`, and CLI release run `33275625172` passed,
including post-publication signature, provenance, SBOM, checksum, and exact-
manifest verification. Its canonical manifest identity is
`sha256:38decaa02a3c36d3989a5b3b0d43d267b3f70916813463810f719499ddbeddf1`.
The confirmation-bound RC.30-to-RC.31 transition and homelab GitOps revision
`6daa3101f3ef7fe4134e9132867c600504b336fa` completed without weakening the
CNI deny path. The immutable qBittorrent adapter trust record was replaced by
exact UID precondition and recreated with the matching RC.31 digest. Doctor,
gateway DNS/tunnel readiness, lease delivery and acknowledgement, TCP/UDP
listeners, cluster/external DNS, independent external TCP, qBittorrent
connection/DHT, and privacy-checked metrics passed the initial live checkpoint.

One short pre-epoch collector run is preserved as invalid because its generated
DHT collector retained an RC.23 Argo revision comparison; it recorded no
product failure and contributes no duration. The unchanged-artifact RC.31 epoch
that began at `2026-08-29T21:59:40.8239325Z` is also invalid. It recorded an
adapter-reference convergence loss and handoff generation 249 to 250, followed
by a single UDP AAAA `SERVFAIL` from the Gluetun/CoreDNS path that immediately
withdrew gateway readiness and advanced the lease from 250 to 251. No Pod UID,
restart, release, mapping, or GitOps identity changed, recovery took 1.183
seconds for the DNS event, and the retained independent external TCP checks did
not fail. The strict audit correctly remains unhealthy because the two events
cannot be represented as one bounded handoff.

The DNS defect is in Waycloak's semantic probe, not in CoreDNS protocol serving:
transport failures receive up to three attempts, but a syntactically valid
`SERVFAIL` response was returned immediately without using that same bounded
attempt budget. The successor source retries only `SERVFAIL`, over the same
transport and within the existing three-attempt/three-second observation bound.
It still fails closed on a persistent `SERVFAIL`, every other unsuccessful
RCode, an invalid response, or exhausted transport attempts. This is bounded
observation, not readiness hysteresis, and adds no dependency; the already
qualified `golang.org/x/net` DNS message implementation remains in use. A new
exact candidate, GitOps transition, controlled fault evidence, and fresh
minimum 72-hour unchanged-artifact soak are required.

The first handoff is also a Waycloak defect rather than a provider rotation.
The immutable `WorkloadAdapter` trust record had been recreated at 21:52:10Z;
its Service and exact adapter Pod stayed stable, but its EndpointSlice/status
converged at 22:11:10Z. The lease controller checked adapter readiness before
persisting the already-resolved application backend into its evaluation. A
non-True adapter status therefore appeared as `backend_not_selected`, causing
an unnecessary drain and handoff increment even though the Service and Pod UIDs
did not change. The successor keeps those identities separate: adapter loss
still atomically withdraws rules and delivery, but retains the endpoint and
handoff generation; after exact withdrawal and adapter recovery, the runtime
may resume only that same target and generation. A real backend identity change
still requires the existing drain-before-successor handoff.

Signed `v0.1.0-rc.32` was published from exact commit
`58b9c3550e483f13c7ba7ed1b0517d266afbb3a0`; runtime release run
`33287364188` and CLI release run `33287364118` passed independent publication
verification. Its canonical manifest identity is
`sha256:dfca9f5d4573c1b3e908d7c8bdc782599605a264cc6537e8a9a46245e4b7bf70`.
The confirmation-bound transition, gateway activation, and homelab GitOps
revision `0c2baf0cf65e7eb45f9332c3823e12f6bd6639a8` completed with exact RC.32
runtime images and fail-closed recovery.

RC.32 qualification then exposed a second adapter-suspension defect before a
soak epoch began. The immutable adapter trust-record rotation correctly made
the lease non-ready, but runtime withdrawal needed more than one reconcile.
The first reconcile marked the endpoint `Draining`; the next classified only
that generic phase and lost the original adapter-suspension cause. Completion
therefore stopped preserving the unchanged Service/Pod identity, advanced the
handoff generation from 255 to 256, and left the lease withdrawn with
`ObservationUnavailable` after the replacement adapter was Ready. This is a
product failure, not provider rotation or soak evidence.

The first successor attempted to record adapter unavailability with `NotReady`,
but live RC.33 validation correctly rejected that as an unstable
`ResolvedRefs` reason under the frozen API policy. The runtime had already
withdrawn while the rejected status remained stale, so RC.33 is also invalid
before any soak epoch. The corrected implementation keeps the stable
`RefNotFound` reason and a controller-owned adapter-unavailable message marker,
then carries that cause across a pending multi-reconcile withdrawal only while
the gateway and exact backend Service/Pod identities remain unchanged.
Completed withdrawal can resume the same endpoint and handoff generation after
adapter recovery. Gateway loss and real backend identity changes retain their
existing handoff semantics. No API field, dependency, readiness hysteresis, or
permissive recovery path is added. A new exact candidate and full controlled
adapter-rotation proof are required.

Signed `v0.1.0-rc.34` was published from exact commit
`cd5b301caaa6c3f336d9ee448f1b555261860391`; its canonical manifest identity is
`sha256:3ff1c8380928bf85736dd3316cd28de1fab7a061d6f0560b90a48741140e871d`.
Runtime and CLI publication verification, the confirmation-bound transition,
homelab GitOps revision `83b92e4b9b8eddd568e905a2e8e1374d91bf774f`,
singleton gateway activation, immutable adapter rotation, DNS load, bounded
metrics privacy, qBittorrent listener/DHT/tracker checks, and native Gluetun
renewal passed. The adapter rotation preserved exact Service/Pod identity and
handoff generation 260 as intended.

RC.34 is nevertheless invalid before soak. A controlled `vpn-engine` restart
withdrew gateway, binding, and lease readiness without changing the gateway or
workload Pod UIDs or handoff generation, but the lease did not recover after
the tunnel and gateway readiness returned. The gateway runtime repeatedly
failed to resolve the stable adapter Service through `127.0.0.1:53`, whose
Gluetun-owned process-local state had been disrupted by the engine restart.
The runtime had inherited the Pod's mutable system resolver even though the
qualified CoreDNS sidecar continued to expose the generic cluster DNS Service
through the stable Waycloak overlay listener.

The successor binds only adapter Service lookups to that existing sidecar
listener using Go's maintained standard-library resolver and an exact IP:port
argument generated from the internal overlay configuration. It adds no API
field, external dependency, cluster-CoreDNS assumption, fallback resolver, or
readiness hysteresis. UDP lookup, TCP resolver routing, failed-lookup recovery,
and exact generated runtime arguments are covered in focused tests. The
successful HTTP probe observed while some status layers were still non-ready
is not treated as proof of ordinary-egress fallback; a successor candidate
must repeat packet-level tunnel-loss proof and demonstrate both fail-closed
withdrawal and automatic same-identity recovery before any new 72-hour epoch.

Signed `v0.1.0-rc.35` was published from exact commit
`225b2d8793845827dbee787132279144e5bd179c`; runtime release run
`33304149150`, CLI release run `33304149139`, and both independent
post-publication verifiers passed. Its canonical manifest identity is
`sha256:05611fbd7dc76d7cbad816230e55ea30cd55c7ae9825b92ef48ce8cf1a88b2ff`.
Confirmation-bound plan
`sha256:a29616d6081142481692c5aaf3101bccb19dc3b9a9e4cae550a23aca4c88d485`
deployed Helm revision 63, and homelab GitOps revision
`b6e190b5b4ab37e713aa872f311f21ea236f6f3c` converged root, Waycloak, and
qBittorrent Healthy/Synced. The immutable adapter replacement preserved the
exact qBittorrent Service and Pod identities and handoff generation 261.

RC.35 is also invalid before soak. The controlled Gluetun PID 1 restart proved
gateway and lease withdrawal with no protected-workload success during a
non-ready observation and preserved the gateway/workload Pod UIDs plus handoff
generation. Gluetun's loopback DNS subsequently resolved external names again,
but CoreDNS continued returning `SERVFAIL` with `no healthy proxies`. Its
forward plugin had marked the sole loopback upstream unhealthy during the
restart; the one-second connection-cached health loop plus
`failfast_all_unhealthy_upstreams` prevented subsequent real semantic probes
from exercising the recovered resolver. Gateway DNS, membership, lease rules,
delivery, and acknowledgement therefore remained safely withdrawn.

The successor disables CoreDNS's independent upstream health state with its
maintained `max_fails 0` option for both generic cluster-DNS and Gluetun
loopback forwarders. Every real query still reaches its sole selected upstream,
and Waycloak's bounded cluster/external A/AAAA UDP/TCP probes remain the only
readiness authority, so transport or response failure still withdraws
immediately with no fallback or hysteresis. The pinned CoreDNS v1.14.7 remains
the latest qualified stable release. The same correction replaces unsupported
Windows PowerShell `Get-Date -AsUTC` and `ConvertFrom-Json -DateKind String`
calls in the gateway-transition collector with PowerShell 5.1-compatible
equivalents, preserving failed RC.35 evidence while allowing the successor
preflight and soak collectors to complete.

Signed `v0.1.0-rc.36` was published from exact commit
`f4dee0a0cc8e24f282162279e3ed4704e56e7d97`; its canonical manifest identity
is `sha256:5998e82a535c228b31d33039f0b786437e54f386fb9d36d774826aec139a4ddf`.
Publication verification and the confirmation-bound deployment from homelab
GitOps revision `5d92fa42ccefeb8fcef391445c86376e0cce2a6e` passed. Controlled engine
restart, CoreDNS recovery, DNS load, qBittorrent listener/DHT/tracker behavior,
UDP ingress/forwarding/tunnel egress packet capture, and ordinary-interface
zero-packet fail-closed evidence passed. One reacquired provider session then
accepted and returned translated TCP SYN/SYN-ACK traffic inside the tunnel but
did not deliver the SYN-ACK to independent external clients; that provider-side
TCP NAT return-path failure is preserved separately and was cleared only by an
exact same-candidate gateway replacement. No RC.36 soak began.

The exact RC.36-to-RC.35 rollback exposed a Waycloak release-ordering defect.
`waycloakctl` deleted the immutable class before the source gateway Pod was
removed and before every node attachment had acknowledged deny-first state.
Three protected HTTPS attempts succeeded early in the class-gap observation;
the retained evidence does not prove ordinary fallback and is consistent with
the still-running source VPN, but ADR 0042 requires unavailability throughout
that gap, so RC.36 fails rollback qualification. The cluster is deliberately
left on exact RC.35 while GitOps continues to declare RC.36 with automatic
runtime sync suspended.

The successor source introduces no new dependency or packet-control mechanism.
It uses the existing native node-agent `LockdownAll` primitive plus maintained
Kubernetes `client-go` DaemonSet/status machinery. Before class withdrawal, the
reviewed successor node-agent image runs with the source release identity under
a plan-bound capability hold, continually retains lockdown, rejects new
ADD/CHECK activation, and publishes authenticated withdrawal observations.
Apply requires the complete DaemonSet rollout and a post-hold, generation-
current withdrawal acknowledgement for every binding. It then stages the
target components, rolls every target gateway to current `Ready=True` while the
hold remains, and removes the hold only in final activation. Exact quiesced,
class-withdrawn, legacy class-replaced, staged, and target checkpoints are
restart-safe and journal-bound. Focused source, chart, and lifecycle tests pass;
a signed successor release and repeated forward/rollback packet qualification
are required before a fresh 72-hour epoch.

## Dependency-backed stabilization direction

ADR 0044 was accepted on 2026-08-26 after the RC.26 local-cluster qBittorrent
evidence localized recurring risk in custom DNS serving, direct provider
protocol renewal, and adapter observation semantics. The frozen Kubernetes API
and fail-closed contract do not change. The next stabilization slices qualify
maintained current dependencies, delegate provider mapping acquisition to
Gluetun, qualify a pinned CoreDNS gateway sidecar, separate adapter Apply and
Observe behavior, and add lightweight OpenTelemetry-first diagnostics with a
no-op default. Issues #242-#246 track the implementation. None of this evidence
completes #123, #140, or #141; a newly published exact release, GitOps
qBittorrent validation, and a clean minimum 72-hour local-cluster soak remain
mandatory before graduation.

## RC.25 local-cluster soak finding and RC.26 correction

Signed `v0.1.0-rc.24` was published from exact main commit
`ee22a1388f48d76171dbd57abf817a706f337721` after runtime release run
`32091226386`, CLI release run `32091226355`, and independent publication
verification passed. Homelab GitOps master commit
`abcba51b6cc00305310187d5921d5b29eae1db86` declared the exact chart,
manifest, eight runtime images, and qBittorrent adapter. Waycloak and
qBittorrent converged Synced and Healthy; all selected containers were Ready
with zero restarts. Gateway, DNS, workload, lease, adapter, and TCP/UDP listener
checks passed, and an aggregate qBittorrent checkpoint observed connected DHT,
active torrents, and known peers without retaining names, hashes, paths,
credentials, or a public endpoint.

The rollout also proved the intended fail-closed behavior of immutable adapter
trust. Argo CD could not patch the existing `WorkloadAdapter` to the new digest;
the controller withdrew adapter and lease readiness on the mismatch. Deleting
only the stale trust record and resyncing recreated it from committed Git,
restored all conditions, and did not restart the qBittorrent Pod. The signed
RC.24 documentation did not describe that required GitOps replacement step, so
its soak began at `2026-08-18T03:08:55Z` only as lifecycle evidence and is not
the final turnkey graduation epoch. RC.25 adds the controller-neutral procedure
and the Argo CD force-recreation caveat without changing runtime, API
schema, generated resources, or configuration semantics. Its exact deployment
must start a fresh 72-hour local-cluster qBittorrent soak.

Signed `v0.1.0-rc.25` was subsequently published from merge commit
`511b210f9e2c800c76813311b868047326fa9902`. Runtime release run
`32096066806` and CLI release run `32096066798` passed their independent
published-artifact verifiers. Homelab GitOps master commit
`779499d6e314ce707f2396bbae0ca5cbb18a63a8` deployed the exact manifest,
chart, eight images, and qBittorrent adapter. The confirmation-bound release
transition and exact-UID adapter trust recreation completed fail closed; all
selected containers returned Ready with zero restarts and qBittorrent retained
its Pod UID.

The authoritative RC.25 epoch ran from `2026-08-18T04:05:45.5042732Z` through
`2026-08-21T04:05:46.0794252Z`: 72.0002 hours and 4,235 samples on the existing
homelab cluster with qBittorrent as the sole application canary. The release
identity, GitOps revision, selected Pod UIDs, and restart counts remained fixed.
The collector recorded no public provider endpoint and 418 of 419 explicit
external TCP results succeeded. Bitmagnet remained at zero and no node was
added, reimaged, or repurposed.

The epoch does not pass stable graduation. Nine samples observed the lease and
TCP/UDP listeners unavailable before a missing provider expiry collapsed the
record into an opaque harness exception. The lease handoff generation advanced
three times. Gateway logs identify 35 fail-closed incidents: 34 split-DNS
observations and one Gluetun tunnel-health failure. The longest DNS interval was
60.123 seconds and affected external A over UDP and TCP, external AAAA over UDP,
and the qBittorrent DNS checks; it was genuine DNS loss rather than a torrent
tracker artifact. All incidents recovered without a selected Pod restart or UID
change.

The deeper write-rate audit found the principal Waycloak defect. The lease
resource version changed between 4,234 of 4,235 minute samples. Eight live reads
four seconds apart then observed eight different provider expiries at one fixed
handoff generation. Runtime logs retained 430 adapter HTTP conflict responses;
429 were qBittorrent listener-probe timeouts. Unchanged reconciliation was
renewing NAT-PMP, rewriting status, and redelivering the same application record
in a feedback loop.

PR #238 bounds provider acquisition to the driver-supplied renewal schedule.
PR #239 runs the unchanged mandatory A/AAAA and UDP/TCP readiness checks in
deterministic sequence and still withdraws readiness at the first failed path;
it adds no hysteresis or fallback. PR #240 preserves the actual failed
lease/listener/DNS condition sample when provider expiry is absent. RC.26 carries
those corrections without changing the frozen Kubernetes API or configuration
contract. It requires exact publication, GitOps deployment, live qBittorrent
validation, and a fresh minimum 72-hour local-cluster epoch before graduation.

## RC.23 documentation-coherence finding and RC.24 correction

Signed `v0.1.0-rc.23` passed its complete PR, main, runtime, CLI, and
independent-publication verification matrix and was deployed from homelab
GitOps commit `18bbd55d`. Its exact local-cluster qBittorrent runtime remained
healthy: all release component digests matched, every Waycloak and adapter
container stayed Ready with zero restarts, lease expiry renewed monotonically,
the handoff and object identities remained fixed, DNS/listener/external-TCP
checks passed, DHT stayed positive, and a credential-contained aggregate
checkpoint observed active torrent upload. No direct or public endpoint value
entered the retained evidence.

That epoch is nevertheless invalid for stable graduation. The documentation
audit found that the signed candidate still called RC.13 current in README and
Getting Started, provided RC.13 direct/Helm/KCL commands, scoped configuration
to RC.11, linked RC.11 notes, and used RC.13 in the canonical soak example. A
new user would therefore consume artifacts that disagreed with the release
metadata and live runtime. RC.23 remains lifecycle, renewal, exact-identity,
and real-torrent evidence, but contributes no graduation duration.

RC.24 is a documentation/status-only correction. It selects one exact RC.24
identity throughout user guidance, removes product-edition wording from the
retained PRD text, and changes no runtime, public API schema, configuration
semantic, or generated Kubernetes resource. After exact CI, publication,
independent verification, and GitOps deployment, it must begin a fresh minimum
72-hour local-cluster qBittorrent epoch. Bitmagnet remains at zero and out of
scope; no node is added, reimaged, or repurposed.

## RC.22 retired qBittorrent endpoint handoff finding

The exact RC.22 GitOps transition on the existing local cluster kept the
gateway and replacement qBittorrent Pod Ready with zero restarts, but exposed a
workload-adapter lifecycle defect before a graduation epoch could start. After
the old qBittorrent Pod disappeared, the gateway runtime first withdrew its
packet rules and then requested application cleanup for the old handoff
generation. The adapter could no longer reach that exact retired Pod to restore
its backend listener, returned conflict, and correctly kept the successor lease
fail closed. The resulting window is retained as rolling-replacement and
fail-closed lifecycle evidence; it contributes no unchanged-artifact soak time.

The correction carries an authenticated `applicationEndpointRetired` fact only
when Kubernetes has selected a different exact Pod UID. With gateway rules
already observed withdrawn, restoration on that retired endpoint becomes
best-effort. Unavailable current endpoints still block withdrawal. A successor
RC must recover the existing qBittorrent canary and then begin a fresh minimum
72-hour unchanged-artifact local-cluster epoch. Bitmagnet remains scaled to
zero and out of scope, and no additional cluster node is required.

Last updated: 2026-08-18

## RC.21 canonical soak finding and node-observation correction

Signed `v0.1.0-rc.21` was published from exact main commit `49fa709b` after
pull-request CI run `32066750279`, main CI run `32068220487`, runtime release
run `32069610654`, CLI release run `32069610644`, and independent published-
artifact verification run `32071070346` passed. Its canonical manifest identity
is `sha256:8e175c3b5c642a92c3f67312d26ea7a9a62178c9e1c76f9949bb45dbd0aceb81`.
The release contains the focused readiness-probe log correction from PR #231;
real-socket mTLS acceptance tests retain genuine certificate diagnostics while
dropping only a bare TCP readiness connection's benign EOF.

Homelab `master` commit `4b7cde03` declared the exact chart, manifest, runtime
images, and qBittorrent adapter image. Confirmation-bound install plan
`sha256:a26e2e40ab46ea9dcbea4a6a936cbdb500760a4b44444c57658ef6aa764dd5b5`
performed the release transition. Root, Waycloak, and qBittorrent converged
Synced and Healthy. The immutable WorkloadAdapter was replaced from committed
Git and became Ready at the RC.21 digest; the qBittorrent Pod retained UID
`fb9ef3d5-70b0-4038-b422-3810e03a55ef` with both containers Ready and zero
restarts. The replacement gateway has three Ready containers and zero restarts.
Bitmagnet remains at zero and no node was added.

Before starting the clock, all gateway, DNS, lease, binding, and adapter
conditions were True. The lease selected the exact unchanged qBittorrent Pod,
covered TCP and UDP, and was delivered and acknowledged. qBittorrent reported
connected with DHT, PeX, and local peer discovery enabled, a positive DHT-node
count, UPnP and random-port selection disabled, and matching TCP/UDP listeners.
External TCP, external DNS, and cluster DNS succeeded. An endpoint-redacted
three-interface packet capture proved external UDP ingress on the VPN tunnel,
forwarding on the Waycloak overlay to the exact qBittorrent endpoint, and
qBittorrent UDP egress through the VPN tunnel. The private pcap SHA-256 digests
are `64c408f830f142ba6f598c63873e293d4a6cd998d371b0d61023e8a79990245b`,
`1a65aa3f0898a7ae146c2172d48ff3d5f55a0eeee1d33a14f220774f505348f2`, and
`357177d03a894cc4632fa9f7fbab063137951b435fa1b3fb7b475c10a9c584a5`.
The short-lived diagnostic Pod ran only on the existing qBittorrent node and
was deleted immediately. Post-start adapter and gateway-runtime logs contain
zero readiness-probe EOFs, warnings, or errors.

The attempted 72-hour unchanged-artifact local-cluster epoch began at
`2026-08-17T22:21:53.7138289Z` but was invalidated at
`2026-08-17T22:24:33Z`. The authoritative collector is the released
`hack/acceptance/real-provider-soak.ps1`, verified byte-identical between RC.21
and current main. It samples the functional path every minute, probes external
TCP every ten samples, continuously watches VPNGateway and DNS transitions, and
writes a deterministic terminal summary. The short earlier RC.21 intervals
remain pre-soak deployment evidence: the clock was deliberately restarted
during instrumentation setup rather than claiming a weaker final record.

Supplementary DHT/API samples, continuous lease and binding-transition watches,
the node-observation heartbeat watch, and the five-minute privacy-checked
metrics timeline overlap the invalidated interval from
`2026-08-17T22:21:53.7138289Z` through `2026-08-17T22:24:33Z` and continue as
post-recovery lifecycle evidence. The opening metrics sample contains 57
samples across five bounded Waycloak families and zero namespace, workload,
node, UID, digest, or endpoint canary matches. Opening canonical and supplementary samples retain exact
release/GitOps identity, all readiness states, matching listeners, connected
DHT, external TCP and DNS, unchanged UIDs, and zero restart increases. The
endpoint-redacted UDP packet capture above was repeated after this authoritative
start.

Continuous evidence then caught a node-agent observation POST spending its
entire nine-second transaction budget in DNS lookup for the controller Service.
The same agent and node boot identity recovered 1.059 seconds later. Binding
conditions became `Programmed=False/Pending` and `Ready`/`NodeReady` Unknown
under `ObservationUnavailable`, then recovered in approximately 97 ms after the
first non-True API event. The lease correctly drained for approximately 1.63
seconds and advanced handoff generation 210 to 211. The qBittorrent adapter
rejected the first replacement delivery when its listener was unavailable, and
one subsequent API sample reported not connected before recovery with DHT
active, the same Pod UID, and zero restarts. Gateway `Ready`, `TunnelReady`, and
`DNSReady` did not transition. This is fail-closed lifecycle evidence, not a
clean graduation interval, and #116 is reopened.

The focused correction retries only transient DNS resolution in one-second
attempts inside the unchanged nine-second publication context. Permanent name
errors and TCP connection failures still return immediately. It never replays a
POST that reached request writing and does not add readiness hysteresis, extend
observation freshness, or delay the existing lockdown deadline. A deterministic
evidence auditor now rejects non-True continuous
conditions, handoff or identity changes, listener/DHT failures, privacy drift,
and incomplete duration. A successor signed release, exact GitOps transition,
and fresh canonical 72-hour epoch are mandatory before graduation.

RC.20 contributed useful lifecycle evidence before it was superseded: 125
functional samples over 2.102 hours all passed, including 13 external TCP
checks, while the continuous binding stream captured two real fail-closed node-
observation transitions. They recovered in 101.3 ms and 86.8 ms respectively,
with `Programmed=False/Pending` and `Ready`/`NodeReady` Unknown under
`ObservationUnavailable`; the minute sampler did not hide or redefine them.
The lease stream recorded no non-True condition. These observations must inform
the RC.21 audit but do not contribute time to its unchanged-artifact epoch.

## RC.20 exact soak start and readiness-probe log correction

Signed `v0.1.0-rc.20` was published from exact main commit `1484cd66` after
main CI run `32055359060`, CLI release run `32056793766`, and runtime release
run `32056793600` passed. Its canonical manifest identity is
`sha256:afb425271f16f35bf749211e55989008624235181fd90ae6f197a03e91bbe11f`.
Homelab `master` commit `447e9895` deployed the exact release through GitOps.
Root, Waycloak, and qBittorrent converged Synced and Healthy; qBittorrent kept
its Pod UID with zero restarts, Bitmagnet remained at zero, and no node was
added.

The clean local-cluster epoch began at `2026-08-17T19:47:01Z`. Its initial
samples retained exact release and GitOps identities, all gateway, DNS, lease,
binding, adapter, and listener conditions, handoff generation 207, and zero
restart or UID changes. An external diagnostic captured inbound UDP on the VPN
tunnel, forwarding to the exact qBittorrent overlay endpoint, and qBittorrent
UDP egress through the tunnel; only redacted flow predicates and pcap digests
are retained publicly. A continuous condition watch additionally distinguishes
provider-expiry renewal writes from node-agent observation heartbeats.

A whole-container log audit then found that the qBittorrent adapter and gateway
port-forward runtime each logged every two-second Kubernetes TCP readiness
connection as `http: TLS handshake error ...: EOF`. The probes, application
traffic, conditions, and restart counts remained healthy, and Kubernetes
recorded no related Warning event. The message is nevertheless misleading and
would create unbounded-looking operational noise during the graduation window.
The focused correction filters only that exact benign TCP-probe EOF at the two
mTLS HTTP servers; certificate failures, unrelated EOFs, and all other HTTP
server errors remain visible under unit tests. RC.20 observations remain
lifecycle evidence, but the successor release must begin a new unchanged-
artifact 72-hour epoch after exact GitOps deployment.

## RC.19 local stabilization finding and node-observation correction

Signed `v0.1.0-rc.19` was published from exact main commit `9bd0c861` after
main CI run `32044822884`, CLI release run `32046793216`, and runtime release
run `32046793236` passed, including independent public-artifact verification.
Its canonical manifest identity is
`sha256:9ec04739734e9ba1d0f09ae15f490facbd98b2439c2957d312a9f7b606ef19d4`.
Homelab `master` commit `a434793f` deployed that release through exact install
plan `sha256:6843d3621c8f7184547dbf01649aa992192bb4c58328f912f6aea922fa36c9a8`.
Root, Waycloak, and qBittorrent converged Synced and Healthy; qBittorrent kept
its Pod and lease UIDs with zero restarts, Bitmagnet remained at zero, and no
node was added.

The first local-cluster stabilization capture recorded 40/40 healthy
qBittorrent samples, zero gateway/DNS/lease/workload/adapter/listener failures,
zero collection, release-identity, restart, or UID failures, and 10/10
independent external TCP successes. The redacted record contains no public
endpoint. It is lifecycle evidence rather than graduation-soak credit because
the stable lease advanced from handoff generation 134 through 140.

The withdrawals align with authenticated node-observation POSTs exceeding the
agent's five-second deadline. Each timeout locks down the exact workload;
binding NodeReady then becomes non-current and the lease correctly drains with
`backend_not_selected`. EndpointSlice, Pod identity, Gateway Ready/DNSReady,
listeners, and sampled traffic remained healthy outside those bounded
withdrawals. The focused successor transaction overlaps independent node and
binding status writes, uses a nine-second single-attempt deadline inside the
server and observation-freshness budgets, and emits sanitized transport phase,
operation latency, failure-count, and recovery-duration diagnostics. A failed
transaction still immediately locks down, and genuinely stale observations
still withdraw readiness. A new exact RC and stable-generation window are
required before the minimum 72-hour local qBittorrent epoch can start.

Main CI for that correction rebuilt the exact patched Gluetun source and passed
all Waycloak-owned source, race, packet, DNS, CNI, recovery, and turnkey jobs.
Its Gluetun upstream suite alone failed twice because `Test_leakCheck` called an
uncontrolled third-party service that changed the JSON type of its `ip` field.
The deterministic dependency gate now excludes only that exact live-service
test while retaining package compilation, every other upstream test, duplicate
reproducible builds, vulnerability/image scans, Waycloak's privileged DNS-leak
and packet suites, and credentialed local-cluster proof. This is test ownership
correction, not a reduced DNS or fail-closed claim.

## RC.18 publication finding and RC.19 resilient uploader

RC.18 exact main CI and CLI publication passed, including the CLI
clean-download verifier. Its runtime workflow completed all artifact,
vulnerability, SBOM, signing, and attestation gates, then GitHub returned HTTP
503 for every final release-upload retry. RC.18's immutable KCL version exists,
so the runtime job cannot be replayed and RC.18 is not deployable.

RC.19 replaces the third-party release uploader with a repository-owned `gh`
transaction. It addresses the exact tag, tolerates a concurrent creator,
retries GitHub API failures with bounded backoff, and uploads with explicit
same-name replacement. OCI immutability remains unchanged.

## RC.17 publication finding and RC.18 release coordination

Signed `v0.1.0-rc.17` points at exact main commit `1836693e`, whose main CI
run `32036897358` passed. The CLI release and its independent public verifier
passed in run `32037908797`, while runtime release run `32037908755` completed
every build, vulnerability, SBOM, signature, and attestation step before its
release uploader lost a creation race with the CLI publisher. A retry correctly
refused to overwrite RC.17's already-published immutable KCL version. RC.17 is
therefore not a complete public runtime release and must not be deployed.

RC.18 makes both publishers identify their shared GitHub release with the
explicit immutable tag name. This removes the ambiguous `refs/tags/...` lookup
that caused the race without weakening OCI immutability or reconstructing
attested assets outside the release workflow. RC.16 remains deployed until the
complete RC.18 runtime and CLI artifacts independently verify.

## RC.16 live lease-churn finding and RC.17 reconciliation correction

Signed `v0.1.0-rc.16` was published from exact main commit `730b4967` after
main CI run `32029515202`, CLI release run `32030850390`, and runtime release
run `32030850539` passed, including independent public-artifact verification.
Its canonical manifest identity is
`sha256:d08ead36d5b81e4712554f7c1a853a015a103bceebf474d5904631eba0667cdc`.
Homelab `master` commit `5410de7a` deployed the exact release through install
plan `sha256:c9d2414a9fc3bbaf3a4b4b9b2af38950d80682ee9443e53b8d80b3b4e4f7b782`.
Root, Waycloak, and qBittorrent became Synced and Healthy; qBittorrent retained
its Pod and lease UIDs with zero restarts, and Bitmagnet remained at zero.

RC.16 eliminated the false gateway DNS observation: two consecutive ten-minute
windows recorded zero Gateway Ready or DNSReady transitions, zero DNS/listener
failures, and 20/20 successful independent external TCP checks. They still did
not qualify as soak evidence because the stable lease UID advanced from
handoff generation 75 through 78 while all periodic functional samples stayed
green. The second window began after rollout settling, so the churn is not an
immutable-resource replacement artifact. Optimistic status conflicts also
continued during routine expiry observations; stale writes were rejected and
never regressed the generation.

The current controller begins reconciliation from the informer-cache lease
copy and only discovers a stale resourceVersion at the status write boundary.
RC.17 begins from the authoritative API reader used by the other lease
dependencies and emits transition-only, endpoint-safe handoff diagnostics for
gateway readiness, missing selection, and Service/Pod identity changes. The
diagnostics contain no provider address or public port. This correction must be
published and deployed exactly, then pass a new stable-generation window
before the minimum 72-hour local-cluster qBittorrent epoch can start.

## RC.15 live DNS-readiness finding and RC.16 observation correction

Signed `v0.1.0-rc.15` was published from commit `1eb78eb0` after main CI run
`32023194452`, CLI release run `32024532561`, and runtime release run
`32024532669` passed. Its canonical manifest identity is
`sha256:f5e85c0afb96d264983c481cf7cc08abf93a217162411c83bb1597ee38244da1`.
Homelab `master` commit `f7c03d2d` deployed the exact release through install
plan `sha256:455b3562ccc8a14f81aacadaa703e44f2ed49d0b5b0d06f5e91f68b8b13a5361`.
The root, Waycloak, and qBittorrent applications became Synced and Healthy,
qBittorrent retained its Pod and lease identities with zero restarts, and
Bitmagnet remained at zero.

A ten-minute pre-soak stabilization window then caught one VPNGateway
Ready/DNSReady withdrawal and recovery even though the gateway agent emitted
no completed DNS-probe failure. The lease advanced from handoff generation 56
to 57 and then 59 as the fail-closed controller reacted. All ten independent
external TCP checks succeeded, qBittorrent's TCP and UDP listeners were present
in every healthy sample, and its Pod identity and restart count stayed fixed,
but the window is lifecycle evidence only and the 72-hour clock did not start.

The cause is an observation race in the gateway agent: every one-second
reconcile published `DNSReady=false` before its five end-to-end probes
completed. A concurrent controller health read could observe that in-flight
value even when the probe set ultimately succeeded. RC.16 retains the last
completed successful DNS observation while a successor probe is in flight and
still publishes false, installs deny rules, and returns an error immediately
when a completed engine or split-DNS observation fails. This is completed-
observation semantics, not permissive hysteresis. The exact RC.15 stabilization
record remains useful evidence of fail-closed withdrawal, fixed application
identity, live TCP reachability, and the defect's recovery, but cannot count as
graduation soak credit.

## RC.14 live upgrade finding and RC.15 lease-status correction

Signed `v0.1.0-rc.14` was published from commit `d9a802d3` after main CI run
`32017729109`, CLI release run `32019002279`, and runtime release run
`32019002294` passed, including independent public-artifact verification. Its
canonical manifest identity is
`sha256:e292855fb9f8d89d9ea851a45ca9f3c6386199c630e98dcd4d1feac65c470057`.
Homelab `master` commit `4e23fec8` deployed the exact chart, runtime images, and
qBittorrent adapter through install plan
`sha256:ea4e15047b039780d6d56dc817807c70f80217a1e320ee61bd8bc8dd56317eac`.
Root, Waycloak, and qBittorrent became Synced and Healthy; the qBittorrent Pod
UID and PortForwardLease UID were preserved, and Bitmagnet remained at zero.

That live upgrade exposed a lease-status concurrency defect before a soak epoch
could start. The restarted runtime and persistent adapter advanced the handoff,
but an older controller reconcile could force-apply stale status without a
resource-version precondition. The lease status regressed to generation 47
while the runtime had already accepted generation 48, so the runtime rejected
later requests as `handoff generation regressed`. All externally observed lease
conditions became `Unknown/ObservationUnavailable`; the system did not falsely
advertise forwarding readiness. An exact status repair advanced 47 to the
already-applied generation 48, after which the controller completed generation
49 and restored all seven conditions. The stable lease UID and qBittorrent Pod
UID remained unchanged.

RC.15 replaces force-owned status apply with a resource-version-checked status
update. A stale reconcile must now conflict instead of overwriting a newer
handoff generation. Focused tests prove both the conflict and preservation of
the newer generation. RC.14 deployment evidence remains upgrade and
fail-closed lifecycle evidence only. RC.15 must be published, deployed by exact
GitOps identity, and begin a new minimum 72-hour local-cluster qBittorrent epoch.

## RC.13 soak finding and RC.14 observation correction

The RC.13 graduation epoch started at `2026-08-17T08:59:18Z` but was stopped
and excluded at `2026-08-17T09:09:52Z`. All eleven one-minute samples were
exact and green, yet the stable PortForwardLease UID advanced from handoff
generation 33 to 34 and then 36. A subsequent raw watch captured another
`VPNGateway` Ready/DNSReady withdrawal from `09:19:50Z` to `09:20:00Z`, followed
by lease generation 37 becoming `Active` at `09:20:02Z`. This proves a
one-minute collector can miss brief fail-closed transitions.

The gateway agent emitted no reconcile-error or recovery transition during the
captured interval, while Gluetun's local DNS-status endpoint returned HTTP 200
throughout except for one delayed one-second sample. Fifteen paired A/AAAA UDP
lookups from the VPN engine also passed 30/30 at 132–161 ms. The evidence points
to the controller-to-agent HTTP observation boundary rather than torrent
trackers or an observed agent DNS failure.

The RC.14 correction reuses a bounded HTTP transport, adds one independent
retry only for transient EOF/reset/closed-idle observation failures, and emits
sanitized transition diagnostics for phase, class, attempts, latency, and
recovery duration. Timeouts, cancellation, HTTP status/payload failures, and a
valid agent response reporting tunnel or DNS loss are not retried. The agent's
immediate fail-closed behavior is unchanged; this is not permissive DNS
hysteresis. The soak collector now records a redacted condition watch in
addition to periodic functional samples and counts every Ready/DNSReady
withdrawal. RC.14 was signed and deployed exactly, but its live lease-status
regression prevented the graduation epoch from starting. RC.15 carries the
focused correction and must start a fresh minimum 72-hour local qBittorrent
epoch before it can contribute graduation evidence.

## v0.1.0-rc.13 staged-generation candidate

RC.13 corrects the handoff ordering defect found during RC.12 GitOps
activation. A new endpoint and incremented handoff generation are now persisted
as `Selecting` before the controller invokes any provider, gateway-rule,
delivery, or workload-adapter effect. A Kubernetes status conflict can
therefore retry only the same durable generation; it cannot leave the runtime
or adapter ahead of lease status. Strict stale-generation rejection remains
unchanged.

Signed tag `v0.1.0-rc.13` points to commit `e875f4d`. Main CI run
`32008451040`, runtime release run `32009694599`, and CLI release run
`32009694506` passed all substantive and independent published-artifact checks.
The canonical manifest identity is
`sha256:64b1ef7a9914ff1a929aa87c4bf5ae331f25018792ccabc56d976c5965b55c8f`.
Homelab `master` commit `26d901b2` deployed it through exact install plan
`sha256:c76dd82c69c5c4eee8dbe209cf930ede346ea5ba9973dcd7beba261532b87de0`;
Helm revision 27 and the root, Waycloak, and qBittorrent Argo applications are
Synced and Healthy.

The live immutable WorkloadAdapter replacement retained qBittorrent Pod UID
`fb9ef3d5-70b0-4038-b422-3810e03a55ef` and PortForwardLease UID
`60938839-4c23-47e2-84d5-e0656664a1f8`. The lease watch observed generation
31 with no active endpoint, a durably persisted generation 32 in `Selecting`,
and then generation 32 `Active` with every lease condition true. Neither the
lease nor workload was deleted, and no cleanup quarantine was required. The
immediate exact-release smoke passed gateway and DNS readiness, both listener
protocols, external and cluster DNS, and independent external TCP with zero
restarts. A 30-second qBittorrent sample recorded 68 inbound and 284 outbound
UDP datagrams with zero `NoPorts` and `InErrors`; DHT is enabled and UPnP is
disabled.

The stopped RC.12 collector contributed 78 lifecycle samples over about 78
minutes with zero restart increases and no public endpoint recorded. It also
captured several fail-closed lease/listener windows, including an approximately
seven-minute pending interval, four collection failures, and the planned
release transition, so it is not clean graduation-soak evidence. The later
RC.13 epoch is also lifecycle evidence only because raw condition history
exposed recurring controller-observation withdrawals and lease generations
33, 34, 36, and 37 while the minute samples stayed green. A successor exact
artifact must start a new clock. Bitmagnet remains at zero replicas and no node
was added. The collector requires the expected version and manifest digest
explicitly and has no stale candidate default.

## v0.1.0-rc.12 support-matrix candidate

RC.12 makes the certified operator boundary part of the canonical signed
release identity. Its one row binds K3s `v1.36.1+k3s1`, Flannel, containerd
`2.2.3-k3s1`, Linux 5.10+, `amd64`, Gluetun with Proton/OpenVPN, the supported
TCP/UDP/DNS/port-forward/adapter features, and the exact evidence-suite names.
The release inventory gate rejects a missing or altered row. Kind, k3d,
`arm64`, and multi-platform image availability remain test or artifact
evidence and do not silently expand this support claim.

Publishing and deploying RC.12 invalidates RC.11 as the final unchanged-
artifact soak epoch, although its samples remain lifecycle evidence. After the
exact signed RC.12 artifacts converge through homelab `master`, a new minimum
72-hour local-cluster epoch must monitor qBittorrent as the sole application
canary. Bitmagnet remains at zero desired replicas and no additional cluster
node is required. Stable graduation remains open until that epoch and its
final evidence review complete.

The RC.12 GitOps activation exposed a lifecycle defect before that epoch could
count as final graduation evidence. Immutable WorkloadAdapter replacement
briefly made the adapter unresolved. The controller attempted to withdraw
durable lease generation 38, while the adapter had already applied generation
39; the adapter correctly rejected the stale withdrawal, packet rules remained
withdrawn, and the lease stayed fail closed. Its declared 10-minute cleanup
bound quarantined the old identity, after which GitOps recreated a new lease
that immediately reached all Ready conditions without restarting qBittorrent.

The cause is an external-side-effect ordering gap: initial successor selection
could be acquired, programmed, and delivered before its incremented generation
was persisted to Kubernetes status. A conflicting status write could therefore
leave the runtime and adapter one generation ahead. The correction stages the
`Selecting` endpoint and generation in status first, then performs runtime
effects on the next reconcile. RC.12 soak samples remain lifecycle evidence,
but a successor candidate is required and starts a fresh 72-hour epoch.

## Declared recovery lifecycle certification

Issue #32's five lifecycle requirements are complete for the published support
boundary. Portable create-only backup/restore, exact forward and rollback
transactions, interrupted-transition recovery, observation-trust rotation,
bounded corrupt-Helm repair, and the cold single-server K3s embedded-etcd row
all run in the exact release gates. RC.11 CI run `31996762388` repeated the
hosted K3s recovery row alongside the release lifecycle and packet suites.

The supported datastore row remains deliberately limited to the documented
K3s topology. Repeating a destructive datastore restore against the homelab or
adding an otherwise unnecessary cluster node is not a Waycloak product gate.
Additional K3s topologies, distributions, external datastores, and operator
environments require their own future support rows and do not inherit this
evidence.

## Turnkey installation certification

Issue #138 is complete through two independently exact gates rather than an
artificial requirement for another cluster node. RC.11 main CI run
`31996762388` completed the full clean-cluster exact-artifact turnkey job in
14m46s, including preflight, confirmation-bound install, chained-CNI receipt,
live capability, doctor, disruptive gateway loss, rollback/recovery, and
owned-fixture cleanup. The same signed manifest and runtime identities were
then consumed through homelab GitOps and the exact installer plan, where the
Proton/OpenVPN qBittorrent canary proved protected egress, fail-closed gateway
replacement, and live inbound TCP/UDP forwarding.

Keeping clean-cluster mechanics and credentialed provider compatibility as
separate gates preserves both claims without making a new infrastructure node
a product dependency. Neither gate may substitute a fixture for the behavior
it owns.

## Clean-break purge certification

Issue #139 is complete. The disposable Kind drill repeatedly exercises exact
metadata-only inventory, refusal on live Pods/finalizers/target drift,
UID-preconditioned partial deletion, idempotent completion, disappearance of
alpha discovery, and fresh replacement installation before the privileged
no-direct-packet suites run on that same purged cluster. The authorized
homelab drill independently stopped the only protected workload and its
runtime process, uninstalled alpha, separately purged its CRs and CRDs, and
re-authored the signed replacement from GitOps with fresh Pod, binding,
allocation, gateway, and provider state.

The observed 301-second normal alpha uninstall is retained as a known bounded
maintenance-window limitation. It did not leave a protected process, import
state, combine uninstall with destructive purge, or permit ordinary fallback,
and therefore does not justify another destructive production drill.

## v0.1.0-rc.11 exact inbound forwarding candidate

RC.10 made the qBittorrent listener and DHT path operational, then an
independent external TCP probe exposed a gateway firewall composition defect.
Proton delivered TCP and UDP packets to the negotiated internal port on
`tun0`, and the lease-specific DNAT rules were present, but the separate
Gluetun and Waycloak baseline forward chains still rejected new inbound
connections. Their established-return rules were correct for outbound flows
but insufficient for provider-initiated ingress.

RC.11 marks a packet only after it matches an exact active lease in the
lease-owned prerouting chain. Both baseline forward chains admit only that
non-routable packet mark from the tunnel to the owned overlay; unmatched
tunnel ingress remains denied. A live diagnostic application of this contract
changed the same independent external TCP probe from closed to open. The
permanent RC must reproduce that TCP result, observe external UDP delivery,
and retain withdrawal, recovery, renewal, application-listener, and DHT proof.

## v0.1.0-rc.10 qBittorrent 5.2 WebAPI candidate

The RC.9 homelab deployment established the complete gateway-to-adapter
network path, including fully qualified Service DNS, cluster-CIDR bypass, and
mTLS. It then exposed an application-contract incompatibility: qBittorrent
5.2 returns HTTP 204 for successful WebAPI operations with no response body,
while the reference adapter accepted only the older HTTP 200 response shape.

RC.10 accepts the documented no-content response for login and mutation
operations, but does not treat that status as authorization. Before changing
the listener it must successfully read the protected preferences endpoint;
after mutation it still requires exact preference observation and a live TCP
listener probe before acknowledging the lease. The live canary must now prove
the provider mapping, TCP and UDP gateway rules, lease acknowledgement,
matching qBittorrent listener, renewal, DHT/peer operation, fail-closed
withdrawal, and recovery.

## v0.1.0-rc.9 gateway adapter DNS candidate

RC.8 recovered the original absent-state withdrawal conflict, then its live
deployment exposed a separate Kubernetes DNS boundary. Gateway Pods use the
Waycloak split-DNS proxy and intentionally do not depend on a Pod resolver
search list. The gateway runtime nevertheless addressed adapter Services as
`<service>.<namespace>.svc`; that short service name timed out from the gateway
while the fully qualified name resolved through the cluster-DNS path.

RC.9 passes the installer-observed cluster domain into the tokenless gateway
runtime and addresses adapters as
`<service>.<namespace>.svc.<cluster-domain>`. The endpoint remains deterministic
and mTLS-authenticated, and the certificate must cover that exact name. This is
a generic WorkloadAdapter transport correction; it does not add application-
or provider-specific behavior.

## v0.1.0-rc.8 adapter handoff recovery candidate

The first RC.7 homelab port-forward activation correctly failed closed but
exposed a recovery defect. An intermediate qBittorrent Pod disappeared after
the gateway runtime had recorded its handoff and before the adapter had
recorded a successful application mutation. The replacement controller then
requested withdrawal of that exact generation, while the adapter treated its
absent state as a conflict. The lease remained indefinitely in `Draining`.

RC.8 makes an absent adapter mutation an idempotent withdrawal success and
durably records delivery intent before calling the application. Exact
generation and Pod-identity mismatches remain conflicts. Runtime and adapter
logs now identify the failed operation and lease without changing the API.
Publication, deployment, and Pod readiness remain insufficient: the RC.8
homelab canary must still prove a provider mapping, TCP and UDP packet rules,
lease delivery and acknowledgement, matching qBittorrent listener, renewal,
fail-closed withdrawal, and recovery.

## v0.1.0-rc.7 optional-capability activation candidate

RC.7 preserves the frozen `networking.waycloak.io/v1beta1` API and extension
contracts. Its distinct exact release identity permits a journal-bound gateway
class replacement that activates the already implemented generic
port-forwarding runtime and qBittorrent reference adapter in the homelab
canary. Publication and deployment are not acceptance evidence by themselves:
the provider mapping, TCP and UDP packet rules, lease delivery, adapter
acknowledgement, application listener, renewal, fail-closed behavior, and
recovery still require live proof before they count toward graduation.

## v0.1.0-rc.6 lifecycle and application-health separation

The first homelab RC.2 transition remained fail closed because its active
primary CNI conflist had already been restored while the valid preserved
`.waycloak-original` file remained. The installer rejected the existing
recovery file before reinstalling the exact chained config, binary, and signed
receipt. RC.3 treats that state as recoverable only when the existing regular
backup is byte-for-byte identical to the active unchained config. Any mismatch
still fails before mutation. The frozen `v1beta1` API and RC.2 plugin contracts
are unchanged. RC.3 runtime publication was blocked before signing by four new
Go standard-library advisories affecting Go 1.26.5. RC.4 pins builds to fixed
Go 1.26.6 through the toolchain directive while retaining a setup-compatible
1.26.5 language floor; no vulnerability exception was added. RC.5 makes an
interrupted Helm repair idempotent when Helm reuses the deleted failed
revision number for the successfully deployed target. Resume still requires
the immutable repair journal and verifies the complete exact target before
removing either lifecycle journal.

RC.6 also limits release completion to release-owned gateway evidence. An
exact target gateway Pod and a current `VPNGateway Ready=True` observation
remain mandatory, but an application-local binding that was already unready
because its Pod or storage is unavailable no longer blocks a product upgrade.
Bindings remain fail closed and continue to report their own readiness.

## Replacement architecture decision set

The project now has a proposed clean-break stable/turnkey architecture in
[stable-turnkey-product.md](docs/product/stable-turnkey-product.md),
[kubernetes-api-maturity.md](docs/architecture/kubernetes-api-maturity.md), and
[stable-product-plan.md](docs/implementation/stable-product-plan.md). It replaces
the alpha gateway annotation with typed `VPNEgressRoute` intent plus one
Pod-template enrollment label, and replaces injected networking containers with
creation-time chained-CNI enforcement and a node-owned data plane.

No backward compatibility, object translation, conversion webhook, or imported
alpha runtime state is planned. The installed v0.3.x implementation described
below remains the as-built system during the clean replacement; it is evidence
and teardown input, not the stable API baseline.

Implementation is tracked by [#123](https://github.com/Amoenus/waycloak/issues/123)
and its dependency graph [#124–#141](https://github.com/Amoenus/waycloak/issues/124).

## Plugin contract stabilization

ADR 0043 now fixes the extension responsibilities without changing the frozen
`networking.waycloak.io/v1beta1` API. Gluetun remains Waycloak's VPN engine and
owns ordinary provider support. The first optional provider mechanism is a
narrow Proton NAT-PMP port-forward capability selected by the Gluetun adapter
only for compatible native configuration; it is not a Proton VPN adapter. The
generic runtime owns lease identity, backend selection, stable-port
translation, rules, handoff, observation, and fail-closed withdrawal.

`WorkloadAdapter` remains an immutable, explicitly selected, out-of-process
last resort for application behavior that cannot consume fixed-port
translation, a standard protocol, or the neutral lease record. The installer
enables only the generic adapter protocol and no longer names qBittorrent in its
activation flag. qBittorrent remains the evidence-backed reference exception,
not a baseline requirement or a provider-extension boundary.

## v0.1.0-rc.1 publication

`v0.1.0-rc.1` is published as a signed prerelease from exact commit
`aee6fbe315ba05f259d1575097d01aab58127ac1`. It freezes the existing
`networking.waycloak.io/v1beta1` contract without changing the API version and
documents one product, its supported use cases, configuration requirements,
deployable resources, and fail-closed operating boundaries. Canonical manifest
`sha256:eb7124723185a5cb3b035f499e01b982cb72ab2bb1009221921bcafb9f190bcb`
binds the complete signed image inventory, Helm OCI chart, and KCL OCI schema
module. The release also carries CLI binaries, downloadable chart and KCL
archives, distinct runtime/CLI checksum inventories, SPDX SBOMs, signatures,
and provenance.

Exact-main CI run `31702064253` and CLI publication/redownload run
`31703359738` passed. Runtime publication completed in run `31703359740`; its
consumer job exposed only an incorrect assumption that the schema-only KCL
module was a root executable. PRs #214 and #215 corrected dependency-based
consumption. Read-only independent run `31706349134` then verified every
published checksum, signature, SBOM attestation, provenance statement,
platform index, Helm/KCL identity and tag digest, plus a real external KCL
dependency render, without republishing or moving the tag.

This RC designation does not waive stable-graduation gates. Multi-day soak,
remaining real-provider and lifecycle evidence, dependency #32, and the final
v1 review remain open. No additional cluster nodes or other homelab
rearchitecture are part of the RC scope.

## One-product release publication correction

PR #206 removed the unreleased split installation/profile model: Waycloak is
one product with baseline fail-closed egress and explicitly advertised optional
capabilities. The current #140 slice replaces the historical `core.*` preview tag,
workflow, helper, verifier, asset, and checksum names with normal Waycloak beta
and stable SemVer publication. Every new release carries the complete signed
nine-image amd64/arm64 inventory; artifact presence does not activate port
forwarding. Historical six-image preview manifests are not accepted by the new
publisher or installer. `Core-v1` remains only the baseline conformance-suite
identifier inside the exact release manifest.

The first one-product beta, `v0.1.0-beta.1`, is published from exact commit
`5e43a8f65b1979a290a85dd7d4346f828d885f04`. Release run `31512800466`
independently verified the signed chart, CLI, complete eight-image amd64/arm64
inventory, SPDX SBOMs, provenance, checksums, and canonical release manifest
`sha256:537d4c5b4b9a0c011c968d4fbe4ce16293c64e1b89a7869d9023f264effe915e`.
This is beta evidence, not stable graduation.

The corrective `v0.1.0-beta.2` release is published from exact commit
`b1e4f442ef812bf091538648c1a48a486aa33922`. Runtime/chart release run
`31525789848` and CLI release run `31525789860` independently verified the
signed chart, CLI, complete amd64/arm64 image indexes, SPDX attestations,
provenance, checksums, and canonical release manifest
`sha256:5ab74387086d85bdd4f75a5df2895d4ba219bd3e8b92e14b930d408b8b62f06e`.
Consumer-side verification repeated every OCI signature, attestation,
provenance and platform check from downloaded release assets. This remains beta
evidence, not stable graduation.

The focused DNS-readiness correction is published as signed
`v0.1.0-beta.3` from exact commit
`7f1c39b75311b387ba667fb31e1f1e0ea14f7e4a`. Main CI run `31676446779`
passed every substantive row. Runtime/chart run `31677523892` and CLI run
`31677523895` each passed publication plus independent public-redownload
verification for canonical manifest
`sha256:3ba8b228175da8ca39d6eedf8dd61b5fde3465027c69d3fb4523bd854a82bff4`.
Homelab GitOps PR `Amoenus/homelab#1578` and exact installer plan
`sha256:b7499bed0c4199a89061a1f1c1045bb5064c0557cfd4f653ba426858e7f4a1cd`
advanced beta.2 to beta.3 in 42.8 seconds. Argo CD is Healthy/Synced, the
qBittorrent Pod retained its UID and zero restarts, and the replacement gateway
runs the exact beta.3 engine and agent digests with both containers Ready and
zero restarts. The transition-only diagnostics separated one 1.044-second
Gluetun HTTP 500 recovery from DNS readiness. A later soak interval correctly
withdrew fail-closed readiness after the cluster-name TCP and UDP probes each
exhausted three one-second attempts. Kubernetes simultaneously marked the
`raspberrypi` agent `NodeNotReady` for about 14 seconds and restarted its
CoreDNS replica after slow local health checks and a liveness failure. This is
genuine cluster-DNS/node loss, not the original ambiguous external-UDP
observation and not a reason to weaken Waycloak readiness. Homelab issue #1580
tracks the infrastructure blocker. The sustained DNS soak remains open and
this is not stable graduation.

## First clean-break beta homelab canary

The authorized homelab drill stopped the sole active protected workload,
verified no enrolled application Pod remained, uninstalled the alpha runtime,
separately purged the exact alpha CR instances and CRDs, and installed the
signed beta from a confirmation-bound clean-install plan. The install apply
completed in 20.3 seconds, then GitOps re-authored the new class, gateway,
route, and workload from scratch. No alpha object, allocation, lease, runtime
state, annotation, or compatibility path was imported. Normal uninstall took
301 seconds; that bounded maintenance-window limitation is retained in the
runbook, while destructive purge remains a separate, explicitly enumerated
operation.

qBittorrent is the sole active workload canary. Its replacement Pod has one
application container, no Waycloak init or sidecar, no added capability, no
Waycloak host mount or credential, an exact UID-bound binding, and zero
restarts. Bitmagnet is intentionally held at zero replicas and is outside this
canary; Qui remains removed. Twelve consecutive external DNS and HTTPS samples
succeeded, and every qBittorrent public-egress observation matched the live
gateway while differing from ordinary egress.

Two beta.1 gateway-Pod replacement tests observed ten blocked outbound probes
during loss and zero ordinary-egress matches. They exposed a release-blocking
status defect: the binding retained `Ready=True` for three blocked probes after
`VPNGateway Ready=False`, because a fresh node observation was sufficient until
its 30-second TTL expired. PR #209 corrected that dependency and beta.2 carried
the fix through the complete signed release gates.

The authorized beta.1-to-beta.2 homelab transition completed from an exact
confirmation-bound plan in 31 seconds. Argo CD converged Healthy/Synced at the
reviewed beta.2 chart, and the controller, CNI installer, node agent, class and
gateway template all carried the exact beta.2 identities. A corrected
qBittorrent-only gateway replacement monitor then recorded 41 samples: 28
protected successes, 13 fail-closed denials, zero observations matching
ordinary egress, six binding-not-Ready samples, two distinct gateway Pod UIDs,
and recovery without replacing or restarting qBittorrent. The replacement
gateway ran both exact beta.2 images with zero restarts. A later 16-request
two-endpoint series returned the same VPN identity on all 15 successes and one
public-observer timeout. Two transient split-DNS health timeouts briefly
withdrew gateway and binding readiness after replacement, then recovered; this
is fail-closed but remains churn evidence rather than an unexplained-outage-free
soak. A subsequent five-minute watch recorded 36 protected successes, two
public-observer timeouts, zero direct-egress matches, and one 17-second
gateway/binding readiness withdrawal while successful traffic still used the
VPN. Gateway logs tie the withdrawal to repeated UDP and occasional TCP
split-DNS observation timeouts. The canary is safe but not yet churn-free; #116
and #141 remain open for root-cause correction and sustained evidence.

That transition also exposed a #140 lifecycle gap: successful install apply
verified the controller's target gateway arguments and the `OnDelete`
StatefulSet template, but did not activate an already-running source gateway
Pod. The current focused correction inventories exact UID-owned gateways,
deletes only stale Pods with UID preconditions, waits for target-revision image
and live `Ready` observations, and resumes the same rollout from the immutable
transition journal before declaring completion.

The multi-day soak, arm64 conformance row, supported
real-provider first-use timing, destructive reinstall certification,
port-forward handoff, remaining #32 lifecycle evidence, beta-cycle hold, and
v1 graduation review also remain open.

## Core.18 through Core.20 homelab release-transition evidence

The signed `v0.0.0-core.18` CLI executed the supported journal-bound transition
from exact Core.17 Helm revision 20 to staged and active Core.18 revisions 21
and 22. The default class changed from reviewed UID
`269917aa-554b-411b-9f73-a840c8a322a2` to
`eed747c2-17ec-4ae8-ad8a-70ee2743eb62`, while both observation Secret UIDs were
preserved. The target class, controller, CNI receipt/installer, node agent, and
Argo declaration converged to the signed Core.18 manifest; `waycloakctl doctor`
reported the selected amd64 node and every class/gateway/route/binding condition
current and Ready.

A concurrent six-minute qBittorrent canary recorded 78 samples: 77 external
HTTP successes, 75 protected egress successes, three denied egress probes, one
HTTP failure, zero successful observations matching ordinary workstation
egress, zero qBittorrent Pod UID changes, and zero container restarts. The
denials coincide with four sampled gateway-not-Ready observations and preserve
the fail-closed invariant.

The Core.18 row exposed that the generated singleton gateway StatefulSet omitted
`spec.updateStrategy`, so Kubernetes defaulted to `RollingUpdate` and activated
the Core.18 gateway template during the release transaction. ADR 0042 requires
that activation to be explicit.

Core.19 corrected that defect and passed exact publication plus independent
verification in run `31467923529`. Its signed CLI planned the deployed Core.18
Helm revision 22 from reviewed class UID
`eed747c2-17ec-4ae8-ad8a-70ee2743eb62`, applied target revisions 23 and 24 in
33 seconds, replaced the immutable class with UID
`38b830bb-82b2-4e64-857d-30f2adecbb91`, and preserved both observation Secret
UIDs. During that transaction, the exact gateway Pod UID
`fab94d47-b355-4277-815e-76a7f5cd9848` and its Core.18 images remained live
while its StatefulSet changed from `RollingUpdate` to `OnDelete` and staged the
distinct Core.19 engine and agent digests. Sixty qBittorrent samples recorded
58 HTTP successes, 58 protected egress successes, two HTTP failures, two
fail-closed protected denials, zero ordinary-egress matches, zero workload Pod
replacement, and zero workload restart.

The separately monitored activation deleted only that reviewed gateway Pod.
Its replacement UID `473eeddd-b038-4c9a-8067-fd8de07c13af` ran both exact
Core.19 images with zero restarts. Seventy-five qBittorrent samples recorded 75
HTTP successes, 72 protected successes, three fail-closed denials, zero
ordinary-egress matches, zero workload Pod replacement, and zero workload
restart. Two samples observed gateway outage and 71 observed the replacement
fully Ready. The current-generation class, gateway, route, and UID-bound binding
then reported Ready; the amd64 host receipt identified the exact Core.19
release manifest; and Argo CD converged Healthy/Synced while ignoring only
Helm's two class-ownership annotations. At that Core.19 checkpoint, corrected
forward transition and explicit real-provider activation evidence were
complete; exact rollback and the remaining #140/#141 rows were not.

The exact-rollback implementation slice binds each controller-generated
gateway Pod template to its signed release version and manifest digest. The
turnkey lifecycle row deliberately reuses identical gateway engine and agent
images across two different release manifests, proving `OnDelete` stages a
distinct target identity while the live singleton retains its source identity
until explicit activation. These controller-owned runtime annotations are
rollout evidence only; they are not an alpha bridge or a user configuration API.

Core.20 published that implementation from commit
`b8d6184be7a7583e48f5f6cc673ad7495044fe82`. Core release run `31476919613`
and CLI release run `31476919685` independently verified the signed
multi-platform images, chart, CLI, SBOMs, provenance, checksums, and canonical
release manifest
`sha256:3eb8e71ce1563bef8c085bc29cd5ff1c6eda8fc261a9810ccc037905a34e15a3`.

The real-provider homelab then completed an exact
Core.19-to-Core.20-to-Core.19-to-Core.20 sequence. Each signed release
transaction retained the source gateway Pod while staging the target
`OnDelete` revision. Explicit activations created fresh Pods on the reviewed
target revisions. Across the completed recorded transition and activation
monitors, qBittorrent kept one Pod UID with zero restarts and no protected
observation matched ordinary egress. The final Core.20 activation recorded
75/75 UI successes, 72 protected successes, and three fail-closed denials
before the exact Core.20 gateway, current-generation conditions, signed doctor
report, and Argo CD all converged Ready/Healthy/Synced.

The Core.19 rollback activation had a materially slower availability recovery:
only 25 of 75 protected probes succeeded while 50 remained denied. A later
ten-sample check succeeded nine times, and the final Core.20 activation
recovered after only three denials. Node-agent verification timeouts and brief
gateway engine-health failures were recorded without a direct-egress match.
Issue #116 therefore remains the sustained real-provider churn investigation;
this lifecycle evidence does not claim an unexplained-outage-free soak or close
issues #140 and #141.

## Clean-break implementation progress

The #124 creation-time CNI feasibility gate passed and merged in
[#143](https://github.com/Amoenus/waycloak/pull/143). It adds the chained CNI
lifecycle, exact attachment state, deny-first failure path and privileged
packet proof. The authorized k3s/containerd/Flannel homelab row and the pinned
k3d row pass with zero direct TCP, UDP, DNS/UDP, DNS/TCP or fragmented-UDP
packets during failed `ADD`; pinned Kind/kindnet also passes while containerd
is restarted during `ADD`. ADR 0034 therefore records a support decision for
the tested matrix.

Issue #125 passed and merged in [#144](https://github.com/Amoenus/waycloak/pull/144).
ADR 0035 and the updated threat model define the node-wide privilege boundary,
read-only Kubernetes scope, host-access matrix, exact identity checks,
unsupported categories, and authenticated `cni-node/v1` protocol. The tested
slice adds a root-only rotating node key, mutually authenticated envelopes,
freshness/replay/size bounds, and abuse tests while retaining denial during
agent or key restart.

Issue #126 passed and merged in [#145](https://github.com/Amoenus/waycloak/pull/145).
Its machine-readable removal ledger names
the alpha APIs, markers, injected runtime, persisted formats, status/events,
RBAC, generated/chart/KCL surfaces, examples, tests, and historical evidence.
The repository audit rejects any unlisted alpha artifact and permits only the
three reviewed dispositions. Runtime deletion remains ordered under #135 after
replacement route, binding, and node-agent Core conformance; the destructive
sequence keeps protected workloads stopped and separates normal uninstall from
explicit CR/CRD purge.

Issue #127 is complete and merged. The accepted API target is
`networking.waycloak.io/v1beta1` on Kubernetes 1.36+, with six role-owned kinds,
one explicit Core route parent, controller-only UID bindings, common conditions,
bounded finalizers, exact SSA managers, and no Core webhook or Gateway API CRD
dependency. The machine-readable freeze and negative audit block ambiguous list,
reference, ownership, lifecycle, alpha, or conformance changes.

Issue #128 is complete and merged in
[#147](https://github.com/Amoenus/waycloak/pull/147). Its vertically testable slice generates the
six replacement Go APIs and structural CRDs, persona-separated RBAC, controller-
only `VPNWorkloadBinding` admission defense, Helm CRD/RBAC output, KCL models,
samples, and a deterministic API reference from one schema source. The chart is
deliberately API-only at this boundary: it renders no controller, mutation
webhook, sidecar, init container, allocation ConfigMap, CNI, or node agent, and
must not be used to enroll workloads until the downstream Core path is proven.
Kubernetes 1.36 envtest covers defaults, CEL and immutability, strict unknown-
field rejection, list ownership, status ownership, persona RBAC, binding
admission defense, and deletion behavior. Fresh-install Kind evidence remains
green in the merged exact-artifact CI run.

Issue #129 is complete and merged in
[#148](https://github.com/Amoenus/waycloak/pull/148). It implements the
single `networking.waycloak.io/egress-route` Pod-template lookup key, rejects
alpha annotations and live-Pod enrollment mutation declaratively, resolves the
exact Pod and route UIDs, and reconciles one effective gateway parent through
the common positive-polarity status contract. A present label remains enrolled
and fail closed when its route is missing, stale, rejected, changed, or deleted;
unlabeled Pods remain outside Waycloak.

Issue #130 is complete and merged in PR #149. It implements gateway-side
`allowedRoutes` consent through an uncached authorization reader, preserves
privacy between missing and denied cross-namespace targets, and requeues routes
on Namespace labels, gateway policy/class state, and target lifecycle changes.
The replacement route and lease APIs share tested dependency indexes; the route
controller consumes them now and the replacement lease controller will consume
its mappings in #137 without coupling this work to the alpha lease runtime. Core
intentionally has no `ReferenceGrant` dependency. Binding allocation, node
programming, and creation-time CNI admission remain downstream gates, so this
slice does not yet make the intermediate chart safe for workload use.

Issue #131 is complete and merged in PR #150. It centralizes current-generation
condition construction and reads, freezes stable reason constants, adds
resource-scoped tunnel, DNS, membership, node, gateway-rule, delivery, and
acknowledgement conditions, and proves server-side ownership, concurrent
convergence, semantic no-op suppression, and transition-time stability.

Issue #132 is complete and merged in PR #151. ADR 0037 and exact CI prove its
controller-owned UID allocation, atomic gateway-owned reservation, collision
and exhaustion behavior, restart recovery, desired/applied/live separation,
fresh exact withdrawal, and durable quarantine.

Issue #133 is complete in PR #152. ADR 0038 closes the frozen binding authority gap with a
controller-authored, credential-free `spec.network` projection. The CNI now
passes only an exact binding UID/generation after installing lockdown; the
privileged node agent re-reads Pod and binding authority, owns native
nftables/netlink programming and verification, repairs drift from durable CNI
records, and cleans only an absent exact Pod. Controller/observation-relay loss
withdraws all allow paths and prevents new prepare operations. A Pod-bound
TokenReview relay requires the exact installation namespace and node-agent
ServiceAccount and limits status publication to bindings assigned to the
reporting agent's exact node without granting node status-write RBAC. Unit,
race, Kubernetes 1.36 envtest, generated-artifact and OCI checks, Kind/kindnet,
k3d/Flannel, and privileged packet/DNS/drift/gateway-loss gates passed in exact
CI run 30198139119.

Issue #134 is complete in PR #153. The replacement manager now has independent v1beta1
class and gateway reconcilers; the alpha gateway controller and inline engine
images are not reused. The class controller claims only the exact immutable
Gluetun controller name and requires its runtime release digest, Core feature
set, and conformance profile to match. The gateway controller resolves the
class before features and same-namespace native/credential refs, publishes no
addresses, and keeps programming false for missing, foreign, rejected,
unsupported, unauthorized, or deleted inputs. Credential values are never
copied into status. The chart renders the default class only when a verified
release version and exact manifest digest are supplied. Unit, race, Kubernetes
1.36 envtest, generated-artifact, Kind/kindnet, k3d/Flannel, and live missing,
foreign-controller and unsupported-feature gates passed in exact CI run 30199319231.

Issue #135 is complete and merged in PR #154. The replacement build no longer
contains or links the served alpha API, mutation webhook, Pod injection runtime,
allocation ConfigMap handshake, alpha gateway manager, adapter protocol, alpha
release workflow, or their executable tests and examples. The controller, CNI,
and node-agent dependency graphs are replacement-only. Exact-head CI run
30316164065 passed unit, race, static, Kubernetes 1.36 envtest, deterministic
Helm/KCL/generated output, security, Kind/kindnet, k3d/Flannel, and privileged
packet gates before merge commit 6f5e4b47d76945d43c685609e4d4ba68745359b5.

Issue #136's original behavior passed in PR #155, and its pre-beta
product-vocabulary clean break is complete in PR #208. Kubernetes 1.36 stable declarative mutation
adds one hard CNI-ready selector to enrolled Pods, while validation rejects
host-namespace and direct-node CNI bypass. An authenticated exact-release node
report lets only the controller publish NodeRestriction-protected scheduling
readiness; stale, unsupported, unready, foreign-node, and release-skewed reports
withdraw it. Positive reports additionally require a root-owned release-bound
receipt. The sole protected identity is
`networking.waycloak.io.node-restriction.kubernetes.io/cni-ready`; the old
preview label is neither published nor accepted and has no compatibility alias.
The receipt must match the exact CNI binary and active conflist; the agent mounts all
three files read-only and restores lockdown on mismatch. Admission remains
outside the packet boundary and no admission webhook or admission TLS is
introduced. Exact implementation commit
aaad5b40f73e3abcba656e0ce55bf7f9a3e569c4 passed Linux race/static analysis,
Kubernetes 1.36 envtest, deterministic generated/Helm/KCL output, security
scans, Kind/kindnet, k3d/Flannel, fresh-install admission, and privileged
packet/gateway-loss gates in CI run 30318076473.

Correction commit bc1e66570efde95a4d245cea40ba235bf36eab91 passed the
complete Linux verifier, Kind admission, Kind and k3d creation-time CNI,
Gluetun, exact-artifact turnkey, and k3s datastore-recovery steps in CI run 31509699770. The correction also separates successful `kubectl exec` stdout
from transient stderr diagnostics before decoding durable CNI attachment JSON;
its platform-neutral regression test passes under Linux race instrumentation.
The k3s job's GitHub wrapper lagged after every step, including `Complete job`,
had succeeded, then finalized successfully with the rest of the workflow.

Issue #137 is in progress and remains unadvertised. ADR 0040 freezes a
same-namespace typed Service as identity input only, deterministic sticky
SingleActive EndpointSlice/Pod-UID selection, withdraw-before-successor
handoff, durable gateway-scoped provider-port reservation and quarantine, and
separate provider, rule, delivery, and acknowledgement observations. The
tokenless gateway runtime uses exact-UID TLS 1.3 mTLS, atomically owns inbound
DNAT and symmetric return SNAT, and rejects provider capacity regression. A
separate immutable-digest WorkloadAdapter path verifies one exact
credential-free Pod and supports a qBittorrent-specific declared
provider-assigned application-port capability without changing Service routing
or adding an application sidecar. Unit, Kubernetes 1.36 envtest, exact mTLS,
restart, and privileged TCP/UDP namespace handoff tests pass. PR #199 published
the gateway runtime and qBittorrent adapter as two implementation images in the
complete release inventory. Both are repeat-built for Linux amd64/arm64 and
pass the same digest, vulnerability, SPDX, signature, provenance, and
independent redownload gates as every other release image. New manifests require
the complete nine-image inventory and reject missing, partial, or unknown
inventories. Artifact availability does not enable the chart or change the default
class feature set. #137 remains open for
confirmation-gated deployment and the real-provider SingleActive rolling
handoff proof with zero wrong-Pod delivery and zero ordinary-egress fallback.

The focused #137 deployment-boundary slice adds it without changing baseline
egress. Complete port-forward chart configuration requires an exact
gateway-runtime digest and a named pre-created controller mTLS Secret; partial
configuration is rejected during rendering. Only a gateway that explicitly
requests SingleActive and references a same-namespace `GatewayRuntimeTLS`
Secret receives the tokenless third runtime container and its deterministic
gateway-UID-owned Service. The runtime mounts only TLS material and receives no
Kubernetes or VPN credential. Gateways without the capability remain
two-container Pods, and foreign same-name Services are rejected rather than adopted or
deleted. The qBittorrent adapter remains an operator-authored out-of-process
deployment; the chart configures controller trust but never injects it, while
the immutable `WorkloadAdapter` and observed adapter Pod carry its exact image
identity. This slice is deployment plumbing, not #137 acceptance or permission
to advertise port forwarding as supported.

PR #205 added the confirmation boundary, but incorrectly promoted internal
maturity labels into separate installation profiles. The
corrected surface uses `waycloakctl install plan --enable-port-forwarding` with
one exact Waycloak release manifest carrying the complete nine-image
inventory. It binds a
named immutable controller mTLS Secret by UID plus public CA and certificate
digests, verifies the exact replacement-controller SPIFFE client identity, and
re-observes it before any apply mutation. The same default class retains its
baseline `Core-v1` conformance identity and advertises only the specific
`PortForwardServiceSingleActive` and optional `WorkloadAdapter` capabilities.
Disposable Kind acceptance
covers wrong confirmation, Secret deletion/recreation, exact re-planning,
journal-bound class replacement, controller trust wiring, and preservation of
a two-container gateway when port forwarding is absent. This remains candidate plumbing:
homelab activation and the provider-backed handoff proof are still required.

Issue #138 is in progress on the turnkey bootstrap slice. `waycloakctl` now
implements read-only cluster preflight, exact release-manifest install planning,
confirmation-gated Helm apply, a reviewed Proton/OpenVPN gateway recipe,
condition-safe doctor output, exact-UID disruptive verification, and a
deterministic redacted support bundle. The chart can install the replacement
controller, deny-first chained CNI, node agent, gateway runtime, and exact
default class only from immutable release identities. A privileged namespace
test proves the gateway deny-first TCP/UDP and tunnel-loss path. Read-only
homelab preflight correctly refuses the still-served alpha API. Release-manifest
validation now recomputes a canonical identity across the exact version, chart,
required images, and profiles, rejecting a changed artifact, hidden extra image,
or stale declared digest before an install plan can be built. The mandatory
chained-CNI installer now has an explicit reproducible Linux amd64/arm64 OCI
image target instead of remaining only a chart-level digest reference. A
publisher-only manifest assembler now requires the exact chart plus all eight
Waycloak image identities, rejects missing, extra, duplicate, tagged, or malformed
inputs, computes the canonical manifest identity, and emits deterministic JSON
that the installer loader revalidates. A dedicated disposable Kind/local-OCI
acceptance now exercises that manifest through CLI preflight, plan, exact
confirmation and apply; it rejects a wrong confirmation before namespace
creation, then verifies pinned runtime images, the release-bound CNI receipt and
chain, authenticated node capability, default-class identity, and healthy
doctor output. The same exact-artifact gate now creates a disposable kernel
WireGuard tunnel and HTTPS egress observer, proves wrong-confirmation
non-mutation, distinct ordinary/protected source identity, exact-UID gateway
replacement, protected application startup denial during loss, recovery, and
owned-object cleanup. The fixture uses runtime-generated keys and certificates
and is not a supported provider. The separate credentialed Proton/OpenVPN gate
now runs against the same exact published identity on the declared homelab
support row; together with the 14m46s clean-cluster RC.11 job, this completes
issue #138 without provisioning another cluster node.

Issues #188 and #189 are complete after the live Core.13 qBittorrent canary proved a
gateway-engine coexistence defect. Chained CNI correctly kept the sandbox
without an IP and prevented both application containers from starting, but
Gluetun's priority-99 `FIREWALL_OUTBOUND_SUBNETS` rule selected `eth0` ahead of
Waycloak's overlay return path and its default-drop `INPUT`/`FORWARD` chains
lacked narrow `waycloak0` allowances. Exact temporary health/DNS, overlay-to-
tunnel, established-return, and priority-90 return-path rules allowed the next
CNI `ADD` to succeed with no direct-egress fallback. The permanent slice makes
those rules marker-owned, fail-closed, drift-reconciled, and no-op stable before
the canary can count as release evidence. The exact Core.15 canary now runs
qBittorrent with one application container, no Waycloak sidecar, no Kubernetes
token, no added capability, zero restarts, matching protected/gateway public-IP
hashes, and current route, gateway, binding, and node conditions. Bitmagnet is
intentionally scaled to zero and Qui is retired from the workload canary.

Issue #190 is complete after that canary exposed a restart-recovery ordering
defect. One exact Ready qBittorrent sandbox coexisted with 20 earlier failed-ADD
LockedDown records for the same Pod UID. Flat file iteration verified the live
sandbox and then allowed a later stale record to overwrite the Pod observation
with not-ready. Recovery now groups records by exact Pod UID, permits one live
Ready sandbox, locks down multiple live sandboxes as ambiguous, preserves the
bounded fresh-ADD grace, and removes old missing/reused attempts only after the
current sandbox verifies or exact Pod absence is authenticated. Unit coverage
reproduces both storage orders with 20 attempts, the real file store, failed
verification, restart idempotency, ambiguity, grace, and one-shot group
withdrawal. PR #196 merged as `3f93f827e1d67ef31037297f1bebdd9413dd2ce2` after
all hosted gates passed, including the privileged K3s/Flannel, Kind chained-CNI,
K3s datastore-recovery, real-provider Gluetun, race, and exact-artifact lanes.
The exact `v0.0.0-core.16` artifacts were then published and independently
verified by Core release run `31445104917` and CLI release run `31445104920`.
The homelab K3s/Flannel canary upgraded through the signed lifecycle plan to
manifest `sha256:94c7c1d09effac4afbe5527bbc29bca577e5e9de2b1354da91e0bac75d7e7ba2`.
It reproduced the old 20-record false observation on Core.15, then proved
Core.16 removed all 20 canonical stale records and retained exactly one Ready
qBittorrent attachment. An actual node-agent replacement recovered that
attachment with current True conditions, no condition or transition-time
flapping, and no qBittorrent restart. Protected and gateway public-egress hashes
matched and differed from ordinary same-node egress; the qBittorrent Service,
EndpointSlice, Web UI, and Traefik route returned healthy results. Argo CD
converged Synced/Healthy without replacing the release-bound class or workload.
The signed Core.16 doctor report was healthy. Protected-workload split-DNS
semantics remain separate issue #191.

Issue #191 is in implementation on `codex/issue-191-split-dns`. The vertical
slice binds one unambiguous Kubernetes DNS Service address and CoreDNS-served
cluster domain into preflight and the confirmed install plan, normalizes only
enrolled Pods to `ndots:1`, and adds a bounded gateway protocol proxy. Cluster
suffixes use a dedicated reviewed underlay route; external names use only the
Gluetun loopback resolver. Active cluster/external UDP/TCP A/AAAA/EDNS probes,
truncation retry and separate tunnel/DNS observation keep the gateway allow path
withdrawn on DNS failure. Unit proxy coverage includes concurrent queries,
search-name containment, oversized responses and TCP fallback; exact Kind,
release and sole-canary qBittorrent evidence remain required before closure.
Qui is not a target workload, and Bitmagnet remains intentionally scaled to zero
during this resource-constrained canary phase.

The CLI release workflow now marks prerelease tags correctly and gates a
successful run on a separate hosted runner redownloading the exact asset set and
verifying checksums, both keyless Sigstore bundles, the signing workflow
identity and issuer, exact tag/source commit, SPDX SBOM, and GitHub build
provenance. This remained implementation-only until an exact tag run published
and passed that verification. The `v0.0.0-turnkey.1` prerelease now supplies that
evidence from exact main commit `21ffebea3444f830ec2c9b29acebd9b36a2fd878`:
release run `30360505871` rebuilt every supported CLI twice, published the
signed checksum and SPDX asset set, and passed the separate hosted-runner
download, identity, issuer, tag/source, checksum, SBOM, and provenance checks.
The Core tag workflow now prepares the missing exact deployment candidate from
`vMAJOR.MINOR.PATCH-core.NUMBER` tags. It repeat-builds and publishes the four
Waycloak Linux amd64/arm64 binaries, a vulnerability-gated Gluetun derivative,
and a tag-versioned Helm chart. Gluetun is rebuilt from exact upstream commit
`7eed6eaf160440724a93ca66f66055068cebe4ac` and exact upstream image digest
`sha256:e3272b29a4bc177b389fbdcb54cf9716ccbfc30f04d8b7a35b0a5be9cdb58461`
with only the reachable fixed Go dependencies advanced; the upstream MIT
license and exact dependency patch ship with the artifact. The workflow gates
source, every published image, and the pinned pause image on the vulnerability
policy, attaches SPDX and keyless signatures, records GitHub provenance, and
publishes one canonical manifest containing the six required Core images and,
for new candidates, the complete known gateway-runtime/qBittorrent-adapter
port-forward artifact pair. A
second runner redownloads the release and verifies checksums, identities, SBOM
attestations, provenance, platforms, chart contents, and manifest-to-registry
digest equality through the reusable registry-native verifier.

The immutable `v0.0.0-core.7` candidate was published from exact main commit
`9a78c22633e5bbecc2437742d3740700bbfaa01b`. Core release run `31354593207`
passed publication and a separate fresh-hosted-runner registry-native verifier.
The verifier redownloaded the release and checked the canonical inventory,
checksums, blob and OCI signatures, SPDX attestations, GitHub provenance,
amd64/arm64 indexes, Gluetun source labels and binary checksums, exact chart
layer bytes, and all six replacement CRDs in 3m27s. The companion waycloakctl
release run `31354593216` also passed publication and independent asset
verification. This closes the missing exact hosted-artifact evidence; it does
not substitute for the supported real-provider journey, destructive reinstall
drill, support-row conformance, lifecycle/DR matrix, or soak required by
issues #137–#141.

Issue #32's first portable recovery slice merged in PR #174 with exact green CI
run `31337381340`. ADR 0041 separates coherent distribution datastore snapshots
from a portable logical backup. The `waycloakctl state backup` format contains
only user-authored gateway, route, lease, and adapter specs plus hashed cluster,
CRD, and required class identities. It structurally excludes credentials,
arbitrary metadata, status, bindings, allocations, provider mappings, and live
observations.
Target-bound `state restore plan/apply` repeats preflight, requires exact CRD
and class identity plus namespace prerequisites, refuses every unowned conflict
before mutation, and creates with one explicit field manager without adopting
pre-existing objects. Portable restore always creates new UIDs and reacquires
the live data plane. The merged Kind recovery drill proves missing-route
startup denial, wrong-confirmation non-mutation, new gateway and binding UIDs,
and live protected recovery without imported runtime state.

ADR 0042 and the next #32 slice now define one exact plan/apply boundary for
both forward transition and rollback. Plans bind the live Helm revision,
release and six image identities, exact six-CRD specifications, gateway-class
UID/generation, and stable observation-certificate identities. Apply refuses
source or target-chart drift before mutation, permits only an identical beta
CRD contract, replaces the exact immutable gateway-class UID at its stable name,
preserves release-owned trust identity, and uses a two-revision transition: the
target controller/CNI/class first become Ready with the exact source node agent
retained, then the target agent is activated. This avoids the fail-closed
missing-socket deadlock found by hosted Kind without disabling or bypassing the
CNI deny path. The next hosted run exposed and fixed a restart-recovery cycle in
which durable reconciliation called public CHECK while backend readiness was
still false. The target agent now restores lockdown, authenticates the relay,
reconciles durable state through an internal-only path, and republishes
capability while public CNI calls remain gated. Apply verifies the exact
post-Helm target. Unit/race and a two-immutable-release Kind lifecycle are the
merge gates.
That run also proved `waycloakctl verify` incorrectly reported cleanup complete
after merely accepting route/Pod deletion while three bindings still terminated.
Verification now deletes exact Pods first, waits for Pod and UID-derived binding
absence, then deletes and observes the route; lifecycle health is no longer
allowed to race finalizer-backed withdrawal.
The next exact-head run correctly rejected cleanup instead of producing that
false positive and exposed the underlying failed-`ADD` edge: force deletion can
remove the Pod API object before `DEL`, while the old protocol represented
absence as a generic authority failure and never published zero applied state.
The local protocol now has an authenticated `PodNotFound` result; `DEL` uses the
exact durable attachment to report withdrawal for absent/reused netns state,
retains state on ambiguous failure, and never cleans a foreign namespace.
Hosted run `31344553203` then proved the remaining ordering gap: kubelet sends
`DEL` for a failed sandbox while the Pod is still live, and the recovery loop
discarded its missing netns record before later Pod deletion. Recovery now keeps
that sticky UID enrollment, publishes withdrawal after exact Pod absence or
name/UID reuse, and retires accepted one-shot observations so deleted bindings
cannot make subsequent node reports fail authorization.
Run `31345442701` passed baseline cleanup and exposed the next exact transition
edge: a prior binding generation was rejected as a whole-report authorization
failure, preventing the post-handshake drift loop from adopting current gateway
intent. Older observations for the same binding UID, Pod UID, and node are now
non-mutating no-ops; mismatched or future identity remains rejected.
The third #32 slice implements exact staged-interruption recovery. Before class
withdrawal, apply creates one immutable, non-sensitive release lifecycle
journal containing the reviewed plan and plan ID. Re-planning the same verified
target returns that exact plan. Apply resumes only the reviewed source with its
class withdrawn, the newer target controller/CNI/class revision retaining the
exact source agent, or the completed exact target; missing/foreign journals,
different targets, ambiguous Helm state, image skew, and trust/CRD/preflight
drift are refused. PR #176 merged with exact green run `31347967874`; the hosted
Kind gate interrupted the real CLI after successful staging in both forward and
rollback directions, proved degraded health and no enrolled application
startup/Pod IP, and resumed without repeating staging.

The fourth #32 slice implements explicit observation-relay certificate
rotation. `certificate rotation plan/apply` binds the exact deployed release,
preflight, stable Secret UIDs/public digests, and a durable node-agent rotation
identity. Confirmation precedes private-key generation; the key exists only in
an immutable staged Secret and the non-sensitive immutable journal contains
only its UID and public digests. The controller reloads and validates its
projected pair for every TLS handshake. Rotation publishes bounded old/new
trust, keeps CNI-ready scheduling withdrawn through a node-agent capability
hold, records fresh authenticated non-ready observations through the switched
server and new-only trust, then restores live capability and removes old/staged
material. Unit coverage enumerates every partial Secret/agent checkpoint,
tamper, bundle, cleanup, and later-release carry-forward boundary. Hosted Kind
injects deterministic serving-switch and trust-prune failures. PR #177 merged
after exact-head run `31351170963` passed all seven jobs.

The fifth #32 slice adds explicit recovery for one exact newer Helm revision
left `pending-upgrade` or `failed` by the active journal-bound transition.
`install repair plan/apply` binds the source and stuck Secret name, UID, type,
status, version, and full opaque-object digest without copying Helm payload.
Apply creates a separate immutable journal before UID-preconditioned deletion,
then resumes only the original class-withdrawn, staged, or target checkpoint.
Candidate drift, extra revisions, lost transition authority, and concurrent
install/certificate changes remain hard failures. Unit tests cover deletion and
post-Helm cleanup interruption; hosted Kind corrupts a real staged revision,
kills the repair after exact deletion, proves enrolled startup remains denied,
and resumes to the exact target.

Live Core.7-to-Core.8 homelab deployment exposed an additional exact Helm 4
server-side-apply boundary: Argo CD had acquired fields on rendered runtime
objects, so transition staging failed on field conflicts after the immutable
target class had already been recreated. The lifecycle now invokes Helm with
explicit server-side conflict takeover bound to the reviewed chart and values,
without the broad Helm annotation-ownership override. Repair recognizes only
the exact target-class/source-runtime partial checkpoint, retains the source
node-agent deny path during restaging, and continues to reject any mixed image,
trust, CRD, class, or revision identity. Unit coverage reproduces that partial
state and completes both repair revisions.

The Core.13-to-Core.14 GitOps canary exposed the inverse lifecycle misuse: a raw
Argo sync advanced executable components before its immutable default-class
apply failed. The class remains immutable and `waycloakctl` remains the only
UID-preconditioned transition authority. Connected raw Helm release changes now
fail during rendering before mutation, while an early Argo class wave stops an
offline direct sync before controller/CNI/node resources advance. Turnkey Kind
attempts and rejects raw forward and rollback changes with the class UID, Helm
revision, images, and Ready route unchanged, then runs both supported exact
transactions.

The sixth #32 slice adds the first distribution-native datastore row: a
checksummed K3s `v1.36.1+k3s1` binary, one embedded-etcd server, bundled
containerd/Flannel, and the retained root-only server token. Its dedicated
hosted gate exposed that a warm service-only reset can leave an enrolled
application container running and able to send direct packets. That procedure
is rejected. The supported row enumerates and removes every exact CRI sandbox,
requires zero remaining CRI containers or sandboxes, stops K3s, verifies the
snapshot-bound Waycloak CNI binary/configuration/attachment digest, performs the
documented cluster reset, and installs the exact saved chain as Waycloak-owned
lexicographically first CNI configuration before ordinary kubelet startup.
Exact-head CI run `31361748255` passed coherent Namespace/Pod/binding UID
recovery, fresh sandbox identity, stale `Ready`/`NodeReady` withdrawal, durable
host deny state, restarted node authority, unchanged TCP/UDP/DNS/fragment
counters, no application startup after failed `ADD`, idempotent cleanup, and a
second primary-CNI positive control. This evidence does not generalize to
SQLite, external/multi-server datastores, S3, or another distribution. Those
are explicit future support expansions; they do not require a destructive
restore of the homelab or an additional cluster node to certify the declared
single-server K3s row.

Issue #33 implementation and acceptance are complete on PR #180's exact commit
`9f586011a037a361a9b26ae6445696f7d7f81523`. The replacement controller has a
bounded aggregate Prometheus collector for common acceptance/programming/
readiness, gateway tunnel and DNS health, explicit enrollment state, durable
allocation, lease delivery, collection health, and controller-runtime
reconcile errors. Labels deliberately exclude namespace, object name/UID,
node, network, endpoint, provider, release identity, credentials, and free-form
messages. The chart exposes an optional metrics endpoint and deterministic
plain Prometheus rules/Grafana dashboard ConfigMaps without a Prometheus
Operator runtime dependency. Exact-head CI run `31366217996` passed all eight
jobs, including race, envtest, generated/reproducible artifacts, Prometheus
rule/config compilation, and an 11m26s turnkey Kind journey that observed live
gateway/tunnel/DNS and missing-route fail-closed state while its application
container never started and privacy canaries remained absent from the scrape.

Fresh homelab reassessment on 2026-08-10 reached the authenticated k3s API and
observed all three mixed-architecture nodes Ready. No mutation has begun; exact
replacement inventory, reviewed architecture selection, and the release gate
still precede deployment.

The deployment preparation after the first live alpha churn review found that a
plan could outlive its cluster observation and that mixed-architecture clusters
implicitly activated every eligible node. The current #138 safety slice binds a
canonical hashed cluster/Kubernetes/runtime/kernel/CNI/network observation,
overlay, and explicit architecture into the plan ID; apply re-runs preflight and
refuses drift before creating a Namespace or Secret. Mixed clusters now require
`--node-architecture`, and both the CNI installer and node agent are constrained
to that reviewed row. This lets the homelab begin on its already-proved amd64
row without falsely treating multi-platform image construction as arm64
conformance.

The authorized clean-break homelab deployment subsequently purged the alpha
runtime and installed Core.7 on that explicit amd64 row. Exact lifecycle plans
advanced through Core.8, Core.9, and Core.10; the Core.8 repair exercised the
target-class/source-runtime recovery checkpoint after Helm 4 server-side field
ownership conflicted with prior Argo CD ownership. Fresh Core.10 gateway startup
then proved two narrower Gluetun runtime requirements without allowing protected
workloads to start: OpenVPN must be able to drop from the root supervisor to its
generated non-root user, and Gluetun's DNS server exclusively binds port 53 in
the shared gateway network namespace. The replacement gateway therefore grants
the engine exactly `NET_ADMIN`, `CHOWN`, `DAC_OVERRIDE`, and `SETUID`, while the
gateway agent listens on the already-routed overlay TCP/UDP port 1053 and
forwards only to Gluetun on loopback port 53. The controller remains stopped and
protected workloads remain intentionally scaled down until this correction is
published, independently verified, and installed as an exact artifact.

Core.11 was then published, independently registry-verified, locally checked for
checksums, Sigstore identities, SPDX attestations, and GitHub provenance, and
installed through an exact Core.10 revision-8 transition. It removed both
startup blockers: OpenVPN completed its UID drop and Gluetun exclusively bound
port 53 while the gateway agent bound overlay port 1053. The live gateway still
remained fail closed and exposed two additional pinned-Gluetun integration
requirements. A root supervisor with a UID-1000 OpenVPN child could not signal
that child during health-driven restart without `KILL`; Kubernetes' inherited
`ndots:5` exhausted Gluetun's bounded DNS health window on namespace search
expansions; and Gluetun's current control server correctly returned 401 because
all routes are private by default. A controller-stopped diagnostic proved the
exact follow-up contract: add only `KILL`, retain the cluster nameserver while
setting Pod-local `ndots:1`, and bake a non-secret role policy into the signed
engine image that permits only `GET /v1/dns/status` and
`GET /v1/publicip/ip`. The gateway reached 2/2 Ready with zero container
restarts, while a credential-bearing settings route remained unauthorized.
Protected qBittorrent and Bitmagnet workloads remained at zero replicas.

Core.12 then published and passed both hosted and independent exact-artifact
verification for checksums, Sigstore identities, SPDX attestations, GitHub
provenance, and the `linux/amd64` plus `linux/arm64` OCI indices. An exact
Core.11 revision-10 lifecycle plan installed Core.12 as Helm revision 12 on the
reviewed amd64 row. The native replacement gateway retained the same Pod UID
for a 10.5-minute checkpoint, stayed 2/2 Ready with zero restarts, initialized
OpenVPN exactly once, and recorded no engine errors or health-driven restarts.
All seven gateway conditions were current-generation `True`; the two intended
read-only Gluetun control routes succeeded and the private VPN settings route
remained unauthorized. The diagnostic ConfigMap was unreferenced and deleted.
Protected qBittorrent, Bitmagnet, and qui workloads remained at zero replicas.

Homelab GitOps PR #1525 promoted the exact Core.12 values and digests. Argo CD
applied them without replacing the controller or gateway, but correctly kept
the Application OutOfSync because the Kubernetes API server defaults
`MutatingAdmissionPolicy.matchConstraints.matchPolicy`, `namespaceSelector`,
and `objectSelector` while the chart omitted those stable values. The chart now
renders those defaults explicitly and CI scopes a regression assertion to the
mutating policy. The exact patch release and no-replacement Argo convergence
proof are recorded below.

Core.13 published that exact patch and passed hosted plus independent local
verification of release checksums, Sigstore identities, SPDX attestations,
GitHub provenance, both OCI architectures, embedded Gluetun control policy, and
the chart layer. The later signed Core.14-to-Core.15 lifecycle plan installed
Helm revision 16 on the reviewed amd64 row. Homelab GitOps PR #1532 converged
Argo CD to Healthy/Synced with no diff after the transaction replaced the
immutable default class and gateway under the exact signed plan. The fresh
qBittorrent Pod and UID-derived binding reached current live readiness without
ordinary-egress fallback. Bitmagnet remains intentionally at zero replicas;
Qui is no longer a desired workload.

The live mixed-architecture diagnostic exposed a narrower #138 defect: doctor
treated the two intentionally unselected arm64 nodes as unavailable even though
the reviewed install plan persisted `kubernetes.io/arch=amd64` on both node
components. Doctor now derives its expected set from the live CNI-installer and
node-agent selectors, requires one coherent installation, counts those arm64
nodes as `NotSelected`, and continues to fail for every selected node without a
current authenticated capability. The updated binary reports the live Core.13
row healthy with one `CNICapable` amd64 node and two `NotSelected` arm64 nodes;
arm64 remains uncertified until its separate live conformance row passes.

The turnkey gate additionally found and closed six node bootstrap hazards before
acceptance: installation receipts are isolated from enumerated CNI attachment
state, and the privileged node agent uses the host network namespace so its own
Pod sandbox cannot depend on the not-yet-running local CNI authority. Enrolled
application Pods remain subject to the chained plugin and are rejected if they
request a host namespace. The installer and node agent now also render the same
versioned local socket/key paths, with exact-artifact assertions before any new
Pod sandbox is admitted.
The Core.13-to-Core.14 homelab canary exposed the corresponding upgrade edge:
an old receipt stopped the target node agent, while the ordinary-networked CNI
installer could not create its sandbox without that agent socket. The installer
is now mandatorily host-networked and tokenless. Helm rendering and turnkey Kind
recovery remove the exact socket, restart the installer, verify the exact receipt,
restart the agent, and require authenticated socket recovery without weakening
enrolled-workload denial.
Clean installs now commit a controller-only Helm bootstrap revision before
activating the CNI installer, node agent, and default class. Existing releases
skip that bootstrap; changed releases stage the target controller, CNI, and
class while retaining the exact source agent, then activate the target agent
after the target CNI receipt exists. This ordering was
added after exact-artifact CI proved that simultaneous first activation could
make the CNI authoritative before the controller Pod sandbox existed.
The same gate then proved that an eventually consistent node-agent cache is not
an acceptable CNI identity authority: exact Pod UID and node assignment checks
now use a direct API-server reader, preserving name-reuse and mismatch denial.
The direct reader uses a separately projected Kubernetes API token with the API
server's default audience; the audience-bound observation token remains isolated
and cannot be substituted for Kubernetes API access.

Issue #139 implementation is complete. The teardown assistant creates a
strictly metadata-only alpha inventory with hashed API-server, trust-root, and
cluster-UID identity, canonical target digests, protected built-in workload
owners, and exact Pod UIDs. Destructive apply requires the exact plan ID plus
runtime-empty and separately-uninstalled attestations, re-derives cluster and
target identity, refuses additions/UID reuse/protected Pods/finalizers, and uses
UID-preconditioned CR-before-CRD deletion with idempotent partial retry. The
homelab read-only plan observed four alpha CRDs, seven CRs, 27 protected owners,
and four protected Pods without exposing names, object contents, credentials,
or endpoints. The later authorized homelab drill completed real-alpha
quiescence, separate uninstall/purge, clean replacement reinstall, zero-fallback
checks, and fresh allocation/provider state; the disposable Kind sequence keeps
the destructive behavior repeatable without another production purge.

## Alpha as-built history

The `v0.3.4` sidecar recovery candidate for #121 fixes a fail-closed startup
deadlock found after the qBitTorrent Pod sandbox was recreated while its
allocation projection advanced to a replacement gateway endpoint. The prepare
init container had configured the old VXLAN remote; the verifier loaded the
current allocation but only inspected the stale link, while the conventional
repair agent could not start until verification succeeded. Startup now
reinstalls lockdown and reconciles the latest complete projection before
probing readiness. Endpoint policy routing is also updated before resolving the
replacement underlay so a stale protected default route cannot capture that
lookup. Unit tests preserve deny-first ordering on invalid or failed repair,
the privileged packet test proves stale-to-current endpoint repair, and the
packaged-image lifecycle test recreates the CRI Pod sandbox without changing
the Pod UID, permits a CNI underlay-address rollover, and requires ordinary
egress to remain unavailable until gateway membership and the protected path
recover.

[`v0.3.3`](https://github.com/Amoenus/waycloak/releases/tag/v0.3.3) is the
published Kubernetes-controller correctness patch. A successful
`PortForwardLease` reconciliation previously assigned pessimistic
`Delivered=False` and `Ready=False` conditions in memory before the delivery
observation restored both to `True`. Although the intermediate state was never
persisted, standard condition handling treated it as a real transition and
refreshed `lastTransitionTime` on every two-second observation loop. The
controller now computes the final delivery state once, preserves transition
timestamps while condition status remains unchanged, and lets semantic status
equality suppress no-op API writes. Regression coverage holds both `Ready` and
`Delivered` timestamps across unchanged polls and expiry-only renewals.
Protected release run `30005758278` published the signed artifacts from main
commit `9e7e2f5738a3e1cffcdafc9a4ee896bb8251d46e`. Homelab PR #1469 promoted
the exact manifest identities in merge `2615b21fcebe24a71b390d691bc714c2fa70eb65`.
Live qBitTorrent verification held both transition timestamps unchanged across
multiple 45-second provider renewals while renewable lease fields and resource
versions advanced only on real renewal updates.

`v0.3.2` is the prepared reliability patch for issue #116. It replaces the
`v0.3.1` disabled-keep-alive workaround with a bounded HTTP connection pool
and one independent retry only for transient loopback transport failures.
HTTP error statuses, timeouts, cancellation, and a repeated transport failure
remain authoritative and withdraw readiness immediately. Structured
transition events distinguish recovered transport churn, gateway health
reasons, and temporary use of the last valid projected desired state.

The same patch removes avoidable controller resource writes by using semantic
patch reconciliation, preserves concurrent metadata updates during status
writes, and adds sustained intermittent-engine plus concurrent-update tests.
The release is not evidence that #116 is complete: the digest-pinned homelab
deployment must still complete a multi-hour real-provider soak and quantify
every readiness/lease transition while confirming fail-closed behavior.

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
and rotation proof, and additional-workload certification. `v0.3.2` is a
focused gateway-observation and reconciliation reliability patch for #116;
it does not expand the product API or weaken fail-closed readiness.

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
