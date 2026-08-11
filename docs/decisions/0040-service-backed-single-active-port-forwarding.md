# ADR 0040: Service-backed SingleActive port forwarding

Status: Accepted by issue #137

Date: 2026-07-28

## Context

The replacement API needs a stable workload-owner surface for provider-assigned
inbound ports without carrying the alpha selector, injected sidecar, ConfigMap
handshake, or mutable Pod-name assumptions forward. A Kubernetes Service is a
useful declaration of application backend intent, but normal Service routing
does not prove exact-Pod delivery, return-path source identity, or safe handoff.
EndpointSlice membership can also overlap during a rollout and a Pod name or IP
can be reused.

Provider mapping, gateway packet rules, application delivery, and application
acknowledgement have different owners and failure modes. Treating any desired
state publication as readiness would permit stale status or cross-delivery
during endpoint replacement, process restart, provider expiry, or restore.

## Decision

`PortForwardLease` is an optional, namespaced workload-owner API. Its
`backendRef` is exactly one same-namespace core `Service` plus one named or
numeric Service port. `endpointPolicy` is initially and exclusively
`SingleActive`. The Service supplies identity and port intent only: its
ClusterIP, kube-proxy rules, and load balancing are never on the inbound packet
path and are not evidence of return-path symmetry.

The controller reads the Service and its EndpointSlices from an uncached API
reader. A candidate must come from an EndpointSlice carrying the exact Service
name and controller owner reference with the exact Service UID. The endpoint
must be ready, serving, non-terminating, and target an exact running Pod UID.
Its single IPv4 address must equal that Pod's current address. That Pod must
carry the one Waycloak route-enrollment label and have a current
controller-authored `VPNWorkloadBinding` for the exact Pod UID, route, gateway
UID, and overlay allocation. Pod names, IPs, selectors, list order, and Service
ClusterIP are never durable delivery identities.

Eligible candidates are sorted by Pod UID. An already selected eligible UID is
sticky; otherwise the first sorted UID is selected. Selection is therefore
deterministic even while a rollout temporarily exposes multiple ready
endpoints. The status records the exact Service UID, EndpointSlice UID, Pod UID,
binding UID, overlay address, port, protocol set, and handoff generation.

Handoff is an ordered state machine:

1. select and persist the exact successor identity;
2. if a different generation is active, mark it draining and request exact
   gateway withdrawal;
3. require read-back proof that all old inbound and return-path rules and any
   old application delivery are absent;
4. program the successor's provider/rule/delivery intent;
5. publish `Active` only after the exact current generation is observed.

No successor rule or application record is installed before old-generation
withdrawal completes. Endpoint loss follows the same withdrawal path and
leaves the lease non-ready. A gateway rule replacement is one native nftables
transaction; if the replacement fails, the previous complete rule set is
restored. TCP and UDP may share a provider port only when the provider reports
that capability.

The gateway rules match the exact tunnel interface and provider internal port,
DNAT to the exact binding overlay address and application port, admit only the
post-DNAT tuple toward the protected overlay, and source-translate the exact
return tuple through the tunnel with the provider internal source port. An
unmatched inbound packet on the tunnel is dropped. Read-back markers bind the
lease UID, handoff generation, Pod UID, protocol, and tuple. This direct
tunnel-to-overlay rule set, not Service routing, is the return-path proof.

Provider allocation identity is the `PortForwardLease` UID. The controller
durably reserves a gateway-scoped internal port from 49152-65535 in a
Kubernetes coordination `Lease`, with the exact gateway UID and lease UID in
annotations and digest-only label indexes. Allocation is collision-checked,
idempotent, recoverable after controller restart, and quarantined after
withdrawal or bounded cleanup. A restored or recreated Kubernetes object with
a different UID cannot inherit an old mapping. Proton's initial supported
contract has capacity one and requires one shared TCP/UDP port per VPN session;
capacity or capability regression atomically withdraws inbound rules.

The Kubernetes controller and credential-free gateway runtime communicate over
a narrow versioned HTTPS protocol using TLS 1.3 mutual authentication. Each
gateway has a deterministic namespace-scoped Service identity, while every
request also carries the exact gateway UID so name reuse is rejected. The
runtime accepts only the configured controller SPIFFE URI, has no Kubernetes
credential, and is the sole owner of provider mapping and gateway nftables
state. The controller is the sole Kubernetes status writer.

The distribution chart keeps this runtime boundary disabled by default. Before
provider-backed acceptance, a test installation explicitly enables the
`networking.waycloak.io/PortForwardServiceSingleActive` capability on the same
Waycloak release and default class; its baseline conformance identity remains
`networking.waycloak.io/Core-v1`. Its signed release manifest contains the
complete required and optional port-forward artifact inventory, and its
confirmation-gated install plan binds the exact runtime image plus a
named, immutable controller mTLS Secret UID and public CA/certificate digests.
The client certificate must chain to that CA, permit client authentication, and
contain only the exact `spiffe://waycloak.io/replacement-controller` URI. Apply
re-observes this identity before any mutation. Port forwarding is an optional
capability, not a second product, release channel, or data plane. Public support
requires a new exact release identity after all acceptance evidence below
passes. A `VPNGateway`
must independently request `PortForwardServiceSingleActive` and reference exactly one
`GatewayRuntimeTLS` Secret before its Pod receives the runtime container and
deterministic runtime Service. The container mounts only that TLS Secret, has
no service-account token or VPN credential, and does not appear in a baseline
gateway Pod. Removing the explicit request removes the owned Service and stages
a port-forward-disabled baseline `OnDelete` gateway template; a same-name Service not owned by the
exact gateway UID is never adopted or deleted.

The chart does not deploy an application adapter. It can configure controller
trust for the signed reference adapter, but the network operator must deploy
the immutable-digest adapter out of process and author its `WorkloadAdapter`
trust record. Public port-forward capability advertisement remains gated on the packet,
handoff, and real-provider evidence below; temporary test enablement is not a
conformance claim.

Provider mapping, gateway rules, delivery, and acknowledgement are separate
observations. `Accepted`, `ResolvedRefs`, `Programmed`, `Ready`,
`GatewayRulesReady`, `Delivered`, and `Acknowledged` use the common positive
condition contract and the current generation. `Ready=True` requires one
fresh exact live observation for every applicable stage. Missing observation
is `Unknown`; rejected identity or policy is `False`. No-op reconciliation does
not write status or refresh transition times.

Cross-namespace gateway references require target-side gateway consent and use
the common privacy rules: an unauthorized or unobservable gateway is reported
as an unresolved reference without disclosing whether it exists. Backend
Services never cross namespaces.

An application-specific integration uses an explicitly referenced
`WorkloadAdapter` trust record. The adapter runs out of process, is never
injected, and must match an immutable digest image behind its deterministic
Service. The controller accepts exactly one ready Pod UID and verifies no
service-account token, host namespace, host path, init/ephemeral container,
added capability, privilege escalation, or writable root filesystem. The
adapter receives neither Kubernetes nor VPN credentials. Its versioned mTLS
record and acknowledgement bind the lease UID, generation, Pod UID, mapping,
and expiry; withdrawal must be acknowledged before handoff.

qBittorrent is the first explicit application exception. Its immutable adapter
record advertises `networking.waycloak.io/ProviderAssignedApplicationPort`.
Only that capability lets the gateway atomically target the current provider
public port instead of the Service's fixed backend port. The adapter receives
the exact EndpointSlice Pod address, changes and observes qBittorrent's listener
over application-owned TLS, probes the listener, and reannounces all torrents
before acknowledging. The Service backend port remains the withdrawal target.
The adapter durably records the exact lease UID, generation, Pod UID, address,
ports, and expiry so a restart revalidates and reannounces once, and so drain
can restore the backend port before acknowledging withdrawal. An adapter with
no such declared capability cannot change packet-target port semantics.

The feature remains unadvertised unless privileged TCP/UDP packet tests,
Kind/k3d handoff tests, and the declared real-provider qBitTorrent rolling
replacement test prove no wrong-Pod delivery and no direct-egress fallback for
each support-matrix row. Failure of Service/EndpointSlice identity or direct
return-path proof keeps the port-forward capability unavailable; it never selects
another data-plane backend or ordinary egress.

## Consequences

- Rolling overlap is deterministic and sticky without delegating delivery to
  Service load balancing.
- Pod deletion, name reuse, EndpointSlice drift, gateway replacement, runtime
  restart, and provider expiry withdraw readiness and rules before reassignment.
- The provider mapping can survive target handoff while exact packet and
  application delivery generations remain independently observable.
- Restore and uninstall must preserve or explicitly drain reservation and
  quarantine state; losing it keeps the feature unavailable until old provider
  mappings are known expired.
- Application-specific behavior stays outside the controller and privileged
  gateway process.
- Port-forward availability is deliberately narrower than baseline egress and cannot weaken
  the enrolled workload's fail-closed egress invariant.

## Alternatives rejected

- Route inbound traffic to the Service ClusterIP: kube-proxy selection does not
  bind an exact Pod UID or prove symmetric provider-port return traffic.
- Require exactly one EndpointSlice candidate: ordinary rolling updates would
  become nondeterministic or needlessly unavailable instead of using explicit
  drain and sticky selection.
- Select by Pod name, IP, selector order, or EndpointSlice order: all can change
  or be reused across restart and restore.
- Program a successor before old-rule withdrawal: permits cross-delivery during
  overlap or partial failure.
- Run provider acquisition in the Kubernetes controller: packets would not be
  proven to traverse the selected tunnel and provider networking would enter
  the control-plane trust boundary.
- Inject an adapter sidecar or pass it a Kubernetes token: violates the clean
  replacement ownership and credential boundaries.
- Fall back to a stable sidecar, Service routing, or ordinary egress: violates
  the product invariant.

## Supersession and related decisions

This decision replaces the alpha API, selector, ConfigMap, injected-agent, and
sidecar portions of ADRs 0011-0016, 0018, and 0023. Their as-built evidence for
provider behavior, atomic nftables ownership, application compatibility, and
generation semantics remains informative where restated above. It does not
create a compatibility requirement.

- [ADR 0025](0025-api-stability-and-feature-channels.md)
- [ADR 0028](0028-reference-authorization-and-cross-namespace-consent.md)
- [ADR 0029](0029-common-status-and-condition-contract.md)
- [ADR 0030](0030-capability-advertisement-and-conformance-profiles.md)
- [ADR 0035](0035-node-agent-trust-and-local-protocol.md)
- [ADR 0037](0037-uid-bound-allocation-and-quarantine.md)
- [ADR 0038](0038-binding-network-projection-and-node-observations.md)
