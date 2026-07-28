# Test strategy

Networking claims require packet-level evidence. Unit tests and “Pod Ready” are insufficient.

## Test layers

### Unit tests

- annotation parsing and mutation idempotence;
- stable allocation and quarantine;
- exact UID-derived replacement binding identity, atomic Lease collision,
  exhaustion, restart recovery, stale generation rejection, and durable
  quarantine recreation when the active reservation is missing;
- condition transitions and observed generations;
- provider capability decisions;
- native engine ConfigMap precedence, reserved-key redaction, mount isolation,
  and deterministic rollout digests;
- lease renewal state machine;
- redaction and error classification;
- nftables/netlink desired-state calculation using fakes.

Run formatting, `go vet`, staticcheck, race tests, and fuzz tests for annotation/config parsing.

### Change-sensitive CI

Documentation-only changes use a lightweight gate for Markdown lint, local
links, fenced examples, and changed-file secret scanning. They do not require
the Go/race/envtest/Kind pipeline. Any change to workflows, source, API,
generated manifests, Helm, KCL, build/generation scripts, tests, or dependency
metadata runs the full pipeline. Mixed changes run both relevant layers.

An always-present aggregate check reports classification and selected-job
results so conditional execution cannot weaken branch protection. Unknown
diff bases and workflow-classification changes fail safe to the full pipeline.
Implementation is tracked by issue #64.

### Controller integration tests

Use envtest for:

- CRD validation/defaulting;
- reconciliation and ownership;
- controller restart with persisted resources;
- namespace authorization;
- finalizer behavior;
- status conflicts and retries.
- exact controller-name ownership, immutable release/profile identity, and
  semantic no-op suppression for classes and gateways;
- missing, foreign, deleting, mismatched, unsupported-feature and unauthorized
  gateway references with `Programmed=False` and no addresses;
- credential canaries absent from class and gateway status.

### Cluster end-to-end tests

Stable admission and scheduling acceptance additionally requires Kubernetes
1.36 policy compilation, mutation that preserves user selectors, no mutation
for unlabeled Pods, rejection of host-namespace and direct-node bypass,
`Unschedulable` status on nodes without authenticated Core readiness,
foreign-node and release-skew report rejection, 20-second label expiry, and CNI
refusal when admission or a readiness label is bypassed. Tests must assert that
these paths start no application container and observe no ordinary-egress
packet.

The node agent must also reject missing, writable, symlinked, release-skewed,
tampered, or incorrectly chained installation receipts/artifacts, restore
lockdown, and withhold its positive capability report.

Use Kind for every pull request where practical and k3d/k3s in scheduled/release validation. Tests should deploy an isolated fake egress gateway before requiring external VPN credentials.

Mandatory scenarios:

1. unannotated Pod is not mutated and reaches the normal egress observer;
2. annotated Pod is injected and reaches a different gateway egress observer;
3. annotated Pod cannot reach the internet before overlay readiness;
4. deleting the gateway blocks egress without exposing normal node IP;
5. terminating the tunnel blocks egress;
6. restarting controller leaves data-plane protection intact;
7. agent repairs deleted owned routes/rules;
8. DNS service discovery and external resolution follow configured policy;
9. webhook outage does not affect unannotated Pods;
10. annotated-but-uninjected Pod is rejected;
11. adding/removing members does not renumber allocations;
12. unrelated nftables rules survive agent setup and cleanup;
13. no provider Secret or ServiceAccount token appears in an application container.
14. engine-native ConfigMap changes reconcile, reserved conflicts fail
    `Accepted`, and no Secret file mount appears outside the engine container;
15. legacy and native gateway shapes remain mutually exclusive across API and
    controller version-skew tests.
16. a verified manifest input renders exactly one default Gluetun class while
    absent or malformed identity renders none or fails; a minimal gateway needs
    no Waycloak image digest;
17. class deletion, foreign-controller claims, unsupported features, and
    credential-reference loss produce stable conditions before any data-plane
    object or address is published.

### Port-forward tests

- typed same-namespace Service and named/numeric port resolution through an
  exact Service UID, controller-owned EndpointSlice, Pod UID/address, and
  current UID-bound `VPNWorkloadBinding`;
- deterministic sticky `SingleActive` selection with overlapping rollout
  endpoints, endpoint loss, rapid replacement, Pod name/UID/IP reuse, and
  withdraw-before-successor ordering;
- protocol-faithful provider acquisition, paired TCP/UDP capability checks,
  rotation, renewal, expiration, release, timeout, capacity regression, and
  provider-result failures;
- durable collision-free provider internal-port allocation, restart/restore
  recovery, deletion quarantine, and no reuse while an old mapping can live;
- exact TLS 1.3 controller-to-gateway runtime identity, strict versioned
  messages, exact gateway UID, oversized/unknown input rejection, and no
  Kubernetes credential in the runtime;
- privileged TCP and UDP packet delivery to only the selected overlay address,
  provider-port return symmetry, atomic generation handoff, unmatched-tunnel
  drop, withdrawal, drift, and runtime restart;
- separate provider, gateway-rule, delivery, and adapter-acknowledgement
  observations with current generations, stale-observation rejection, and
  no-op status stability;
- cross-namespace gateway consent and indistinguishable missing/unauthorized
  references; backend Services remain same-namespace;
- qBittorrent compatibility requires an immutable
  `ProviderAssignedApplicationPort` adapter capability, exact EndpointSlice Pod
  address, application-owned HTTPS, listener update/readback/probe, all-torrent
  reannounce, durable restart revalidation, and backend-port restoration on
  withdrawal;
- Kind/k3d rollout tests and real-provider qBittorrent tests prove no wrong-Pod
  delivery, stale advertisement, or direct-egress fallback. Failure keeps this
  Extended capability unavailable.

### Adapter conformance

Every workload adapter image must pass the language-neutral black-box suite
for current, rotated, expired, missing, duplicate, wrong-UID, and stale
generations. The suite verifies exact acknowledgement, bounded retry,
least-privilege execution, and readiness regression without direct-egress
fallback. The qBitTorrent adapter is the reference implementation, not a
special core code path.

### Data-plane backend conformance

Every supported backend passes the same packet-level startup, loss, drift,
restart, detach, upgrade, and cleanup suite. Backend-specific tests supplement
but never replace those assertions. eBPF evaluation must record actual kernel
features, BTF, architecture, CNI, hooks, and verifier results; kernel version
alone is insufficient evidence.

### Stable CNI and node-agent abuse conformance

Every supported CNI/runtime row runs the creation-time proof with the
authenticated `cni-node/v1` channel. Unit/abuse coverage rejects non-root Unix
peers, missing or foreign keys, unsafe key/directory modes, duplicate headers,
stale/future timestamps, replayed request IDs, method/path/body/status tamper,
unsigned or tampered responses, oversized messages, unknown fields, trailing
JSON, UID/sandbox/netns reuse, and foreign cleanup state. Agent restart rotates
the key during `ADD`; bounded retries may recover only while deny remains
installed. Positive-control capture followed by zero direct TCP, UDP, DNS
UDP/TCP, and fragmented-UDP packets is mandatory. Authentication failure is an
availability failure and never permits fallback.

The production agent suite additionally proves that the CNI cannot supply a
data-plane configuration, stale binding UID/generation is rejected before
programming, partial configure or verify restores lockdown, drift repair occurs
under lockdown, and restart rebuilds only from validated durable attachments.
Pod-bound TokenReview tests reject unbound tokens and cross-node observations.
Loss of the authenticated controller observation relay makes local status
unready, rejects new prepare, and locks down every durable attachment before a
recovered path can be re-verified. Only an absent exact Pod permits stale-netns
cleanup; API/watch ambiguity, deletion, node mismatch, and binding revocation
retain deny.

### Failure injection

Capture packets at the protected Pod, node/CNI interface, gateway overlay, and tunnel where possible. Inject:

- gateway Pod deletion;
- gateway node drain;
- tunnel interface removal;
- DNS failure;
- sustained engine-health failure, proving fast readiness withdrawal followed
  by an engine-container-only restart and automatic protected-path recovery;
- CRI Pod-sandbox recreation with a gateway endpoint projection change between
  prepare and verify, proving ordinary egress remains closed and the same Pod
  UID/IP recovers without waiting for the conventional repair container;
- stale desired generation;
- provider API timeout;
- controller/webhook restart;
- CNI packet loss and MTU mismatch;
- node-agent socket loss, wrong key, key rotation, replay and watch/RBAC loss.

The key assertion is absence of direct packets, not only expected application
errors. Recovery tests also assert stable gateway and workload Pod UIDs, stable
overlay and lease identities, and an increased VPN-engine container restart
count before readiness returns.

## Credentialed tests

Provider tests run only in protected CI environments or operator-owned clusters with short-lived credentials. Pull requests from forks never receive credentials. Logs and artifacts are redacted, retained minimally, and must not publish residential/provider-linked public IP history.

The sustained Proton/qBitTorrent procedure is defined in
[real-provider port-forward acceptance](real-provider-port-forward.md). It is
an explicit, gated operator-cluster suite and is not replaced by the
protocol-faithful local fixture.

## Performance tests

Measure gateway CPU/memory, per-agent RSS, throughput, UDP packet loss, DNS latency, reconciliation duration, and disruption during membership changes at 1, 10, and 50 clients. Publish results with node/kernel/CNI/MTU context.

## Release gate

A release cannot rely on manual observation alone. Required suites, artifact verification, supported-platform results, and any accepted failures are attached to the release manifest.
