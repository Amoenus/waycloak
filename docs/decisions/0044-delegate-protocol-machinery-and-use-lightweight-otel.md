# ADR 0044: Delegate protocol machinery and use lightweight OpenTelemetry

Status: Accepted

Date: 2026-08-26

Partially supersedes [ADR 0013](0013-proton-nat-pmp-ownership-and-observation.md)
and amends [ADRs 0007](0007-transparent-gateway-dns.md),
[0018](0018-workload-adapter-protocol.md), and
[0043](0043-engine-capabilities-and-application-adapters.md).

## Context

The exact local-cluster qBittorrent soak proved the fail-closed boundary but
also exposed two avoidable sources of instability:

- Waycloak's hand-written split-DNS server intermittently reports cluster and
  external UDP/TCP failures and cannot always distinguish an upstream exchange
  failure from a local proxy timeout. A c-ares client also exposed a protocol
  compatibility failure that simpler clients and raw queries did not detect.
- Waycloak's custom Proton NAT-PMP loop renews provider state that Gluetun can
  now own natively. Volatile renewal data then propagates into the application
  adapter, which can reconfigure and reprobe qBittorrent despite no mapping
  identity change.

The Linux and Kubernetes packet boundary is not the problem. The official CNI
libraries/plugins, `google/nftables`, `vishvananda/netlink`, `wgctrl`, and
controller-runtime remain appropriate foundations. The opportunity is to stop
owning general DNS serving and provider protocol machinery while preserving
Waycloak's product-specific identity, observation, translation, and
fail-closed semantics.

Operational evidence also needs better failure localization. Kubernetes
conditions and events remain authoritative, but the existing Prometheus-shaped
instrumentation should not force a mandatory Prometheus deployment or two
independent telemetry models. Waycloak must remain lightweight.

## Decision

### Dependency selection and currency

Waycloak prefers a maintained mature upstream dependency over custom protocol
machinery when the upstream boundary can preserve the security contract. Every
new or materially upgraded dependency must record:

- the latest stable release and activity checked at implementation time;
- support policy, security reporting path, license, and maintainer health;
- exact version or image digest, provenance, SBOM, vulnerability policy, and
  reproducibility evidence;
- transitive dependency, binary-size, CPU, allocation, and RSS cost;
- compatibility and rollback evidence; and
- an owner and review date for every deliberate lag from the latest qualified
  stable release.

"Latest" means the latest stable version that passes qualification, not a
mutable tag, branch head, release candidate, or automatic upgrade. An inactive
or maintenance-only package is not introduced when an actively maintained
alternative owns the required boundary. CI reports stale dependencies but does
not silently merge upgrades.

The 2026-08-26 design review found active stable releases for
[Gluetun v3.41.3](https://github.com/passteque/gluetun/releases/tag/v3.41.3),
[CoreDNS v1.14.7](https://github.com/coredns/coredns/releases/tag/v1.14.7), and
[OpenTelemetry Go v1.46.0](https://github.com/open-telemetry/opentelemetry-go/releases/tag/v1.46.0).
These are review baselines, not unconditional implementation pins; each
implementation issue must refresh and qualify the version immediately before
adoption.

### VPN-provider port forwarding belongs to Gluetun

The Gluetun engine adapter will use Gluetun's native, authenticated
[port-forward control capability](https://github.com/qdm12/gluetun-wiki/blob/main/setup/advanced/control-server.md)
instead of Waycloak directly owning Proton NAT-PMP acquisition and renewal.
Waycloak configures only a loopback-bound, least-privilege control-server role
and observes the current mapping through the engine adapter.

Gluetun owns provider selection, provider protocol packets, acquisition,
renewal, and release. Waycloak continues to own:

- durable `PortForwardLease` identity and authorization;
- fixed application-side port intent and Service/exact-Pod selection;
- provider-port-to-backend translation and atomic packet rules;
- observation freshness, staged handoff generations, status, delivery,
  acknowledgement, and fail-closed withdrawal.

If the Gluetun API does not expose provider TTL, Waycloak records a bounded
observation-validity deadline derived from successful observation. It does not
invent or describe that deadline as the provider's lease expiry. Engine API
loss, stale observation, mapping removal, or incompatible native configuration
withdraws readiness and packet rules immediately under the existing freshness
contract.

This supersedes ADR 0013 only where it assigns direct NAT-PMP packet and renewal
ownership to Waycloak. ADR 0013's stable identity, exact observation, generation,
quarantine, translation, and fail-closed requirements remain normative.

### DNS serving moves to a qualified CoreDNS sidecar

The preferred DNS implementation is a separately packaged, digest-pinned
[CoreDNS](https://coredns.io/) sidecar in the gateway Pod network namespace.
It binds only the Waycloak overlay listener and uses the maintained
[`forward`](https://coredns.io/plugins/forward/) machinery for:

- the preflight-bound Kubernetes cluster suffix through the exact cluster DNS
  Service; and
- all other names through Gluetun's protected loopback resolver.

The selected configuration must support UDP and TCP, EDNS0, truncation/TCP
retry, large and fragmented responses, bounded concurrency, connection reuse,
startup ordering after the overlay address exists, graceful configuration
reload, and bounded resource use. CoreDNS process health and upstream reachability
are useful diagnostics but are not Waycloak readiness.

Waycloak retains a small semantic prober. It verifies a known cluster record
and external A/AAAA over the required UDP/TCP matrix through the same overlay
listener used by workloads. One failed required path still withdraws
`DNSReady`, composite readiness, and the allow path immediately. No direct
resolver fallback or permissive hysteresis is introduced.

Before removal of the existing proxy, issue #244 must compare the current
implementation with CoreDNS and the actively maintained
[AdGuard DNS proxy](https://github.com/AdguardTeam/dnsproxy). Only one DNS
server implementation ships. Direct adoption of `miekg/dns` is not preferred:
CoreDNS already owns that protocol dependency, while the v1 repository has
announced maintenance mode and a v2 migration.

### Application adapters separate effects from observations

The workload-adapter responsibility remains application-specific and
out-of-process, but its behavior is separated into three concepts:

1. **Apply** mutates application configuration only for a changed exact lease,
   Pod, target, handoff generation, or public-port identity.
2. **Observe** verifies the application listener and required application state
   with bounded retries inside the existing freshness budget.
3. **RenewAcknowledgement** refreshes acknowledgement of unchanged applied
   state without replaying application mutation.

Expiry-only or observation-validity changes do not reconfigure qBittorrent.
Immutable identity/generation contradictions remain conflicts. Transient API,
authentication, or listener observation failures are reported as transient
unavailability, not HTTP conflict. A last successful observation may be reused
only within an explicit short freshness bound and never across an identity,
generation, port, or withdrawal change.

### OpenTelemetry is the instrumentation model

Waycloak uses the stable OpenTelemetry Go API as the internal metrics and
focused-tracing contract. The default provider is no-op. An optional OTLP
exporter may be enabled with bounded memory, queue, timeout, and drop accounting;
no collector is required for correctness or installation.

The existing Prometheus consumption path may remain as an optional exporter or
bridge derived from the same instruments. Waycloak will not maintain a second
independent Prometheus metric model. Prometheus, Prometheus Operator, an
OpenTelemetry Collector, and any tracing backend remain optional.

Bounded signals cover DNS path/transport/qtype/phase/result and latency,
readiness withdrawals and recovery duration, engine observation age, mapping
and handoff transitions, adapter Apply/Observe results, listener observation
age, and runtime failure class. Telemetry never includes credentials, endpoints,
ports, lease or Pod UIDs, torrent metadata, arbitrary domains, workload names,
or digests as attributes. Export failure can never block reconciliation, extend
freshness, become a readiness input, or weaken fail-closed behavior.

Independent DNS/TCP probes such as Prometheus Blackbox Exporter may be used by
the soak and conformance harness. They are not a Waycloak runtime dependency.

## Responsibility boundary

| Responsibility | Owner |
| --- | --- |
| VPN providers, tunnels, provider port-forward protocol and renewal | Gluetun |
| Split-DNS serving and general DNS protocol behavior | pinned CoreDNS sidecar |
| Semantic end-to-end readiness and fail-closed withdrawal | Waycloak |
| Lease identity, stable target, translation, packet rules and handoff | Waycloak |
| Proprietary application mutation and observation | optional workload adapter |
| Conditions, events and doctor output | Waycloak, authoritative |
| Metrics/traces API and optional export | OpenTelemetry, non-authoritative |

## Migration and acceptance

The frozen `networking.waycloak.io/v1beta1` resources and configuration
responsibility boundaries do not change. Adoption occurs as focused,
independently reversible slices:

1. establish dependency freshness and runtime-cost gates (#242);
2. refactor adapter Apply/Observe/renewal semantics (#245);
3. qualify and replace custom provider acquisition with Gluetun native control
   (#243);
4. benchmark, qualify, and replace DNS serving with CoreDNS (#244); and
5. add bounded OpenTelemetry-first signals and optional export (#246).

Every slice must pass unit, race, envtest where applicable, privileged packet
and DNS-leak tests, generated/reproducible artifact checks, image/SBOM/license/
vulnerability policy, exact publication verification, GitOps deployment, and
the relevant live qBittorrent checks. A fresh exact-artifact 72-hour local-cluster
soak remains mandatory before graduation; earlier failure windows remain useful
lifecycle evidence but do not satisfy the clean epoch.

## Consequences

- Waycloak owns less general networking code while preserving its distinctive
  fail-closed and identity semantics.
- DNS and port-forward behavior track actively maintained upstream projects,
  but exact upstream upgrades become part of the supported release matrix.
- The gateway gains a small DNS sidecar and optional telemetry modules; their
  measured resource cost is an acceptance gate.
- Failures become localizable without making telemetry infrastructure part of
  the control plane.
- Migration requires new exact release and soak evidence; current evidence is
  not retroactively reclassified as clean graduation proof.

## Alternatives rejected

- Continue expanding the custom DNS server: preserves protocol and diagnostic
  maintenance that a mature DNS server already owns.
- Add both CoreDNS and AdGuard DNS proxy: duplicates responsibility and runtime
  cost.
- Keep direct Proton NAT-PMP as the default owner: conflicts with Gluetun as the
  provider boundary and repeats native engine capability.
- Use Gluetun up/down shell commands to configure qBittorrent: combines provider
  and application responsibilities and exposes application credentials to the
  gateway.
- Make Prometheus or an OpenTelemetry Collector mandatory: violates the
  lightweight, Kubernetes-native installation contract.
- Emit per-packet spans or high-cardinality resource attributes: excessive cost
  and disclosure risk.
- Add a generic retry library: readiness timing and fail-closed freshness are
  domain semantics, not a dependency-shaped problem.
- Replace the CNI, require Cilium/eBPF, or alter cluster nodes: outside this
  stabilization work and unnecessary for the observed failures.

## Related decisions

- [ADR 0007](0007-transparent-gateway-dns.md)
- [ADR 0013](0013-proton-nat-pmp-ownership-and-observation.md)
- [ADR 0017](0017-engine-native-configuration-boundary.md)
- [ADR 0018](0018-workload-adapter-protocol.md)
- [ADR 0040](0040-service-backed-single-active-port-forwarding.md)
- [ADR 0043](0043-engine-capabilities-and-application-adapters.md)
