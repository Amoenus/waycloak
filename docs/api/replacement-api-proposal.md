# Replacement API proposal

Status: accepted implementation contract for issue #127; schemas are not yet generated
Last updated: 2026-07-26

## Goals

- Make typed resources the only source of Waycloak policy.
- Separate distribution, operator, application and controller ownership.
- Preserve explicit Pod-template enrollment without a custom annotation API.
- Make unsupported, unauthorized, unapplied and unhealthy states distinct.
- Allow one clean alpha cutover and a credible path to stable `v1`.

The replacement API group/version is `networking.waycloak.io/v1beta1`. The
minimum supported Kubernetes version is 1.36. This deliberately permits stable
`admissionregistration.k8s.io/v1` mutating admission policy for static Pod
placement metadata without carrying a mutating webhook into Core. Kubernetes
1.35 CNI feasibility evidence remains useful but is not a published replacement
support row.

The machine-readable review boundary is
[replacement-api-freeze.json](replacement-api-freeze.json). Issue #128 consumes
that boundary to generate Go types and CRDs; this review does not generate them.

## Frozen API rules

- Every object is structural and has no preserve-unknown escape. Supported
  clients, CI and install paths use strict field validation so unknown fields
  are rejected; a non-strict API-server path may prune them but can never
  preserve or interpret them as Waycloak configuration.
- Every list is explicitly atomic, set, or map and has finite bounds. Conditions
  are map lists keyed by `type`.
- `controllerName`, release identity, trust identity, binding identity, exact
  UID references, and parent-status controller identity are immutable through
  CEL transition rules.
- Defaults are constant and round-trip stable. A namespace is never dynamically
  guessed. Cross-namespace-capable references therefore require an explicit
  namespace, including same-namespace values.
- Core permits exactly one `VPNEgressRoute.spec.parentRefs` entry. Loosening the
  maximum is future additive work; no failover behavior is implied now.
- The six kinds exist because their actors, lifecycle, authorization, or status
  differ. A manager process may reconcile several kinds without changing the
  API model.
- The replacement claims no Gateway API conformance and has no Gateway API CRD
  runtime dependency.

## Frozen feature identities

Core classes must advertise all of:

- `networking.waycloak.io/CoreFailClosedEgress`
- `networking.waycloak.io/TCP`
- `networking.waycloak.io/UDP`
- `networking.waycloak.io/DNSContainment`
- `networking.waycloak.io/GatewayReplacementRecovery`
- `networking.waycloak.io/NodeRestartRecovery`

Extended identifiers are
`networking.waycloak.io/PortForwardServiceSingleActive` and
`networking.waycloak.io/WorkloadAdapter`. Feature lists are set lists. Unknown
required features produce `Accepted=False`, reason `UnsupportedFeature`, before
programming; they never select another backend.

## Kinds at a glance

| Kind | Scope | Writable spec owner | Status owner | Core |
| --- | --- | --- | --- | --- |
| `VPNGatewayClass` | cluster | distribution/provider | class controller | yes |
| `VPNGateway` | namespace | cluster/network operator | gateway controller | yes |
| `VPNEgressRoute` | namespace | workload owner | route controller per parent | yes |
| `VPNWorkloadBinding` | namespace | controller only | controller/node observations | yes, internal-facing |
| `PortForwardLease` | namespace | workload owner | lease controller/manager observations | Extended |
| `WorkloadAdapter` | namespace | operator | adapter controller | Extended |

## Common reference types

Parent references use explicit `group`, `kind`, `namespace`, and `name`.
Cluster-scoped references contain only a name or a cluster reference with no
namespace. Local ConfigMap, Secret, Service, route, Pod, and adapter references
cannot carry a namespace. Cross-namespace references are never inferred.

All user-facing resources implement:

```yaml
status:
  conditions:
  - type: Accepted
  - type: ResolvedRefs
  - type: Programmed
  - type: Ready
```

Each condition has `status`, `reason`, non-sensitive `message`,
`observedGeneration`, and `lastTransitionTime`.

Stable summary reasons are:

| Condition | Reasons |
| --- | --- |
| `Accepted` | `Accepted`, `Invalid`, `UnsupportedClass`, `UnsupportedFeature`, `ControllerNotFound`, `Deleting`, `ObservationUnavailable` |
| `ResolvedRefs` | `ResolvedRefs`, `InvalidRef`, `RefNotFound`, `RefNotPermitted`, `IncompatibleRef`, `ObservationUnavailable` |
| `Programmed` | `Programmed`, `Pending`, `ApplyFailed`, `StaleGeneration`, `ObservationUnavailable` |
| `Ready` | `Ready`, `NotReady`, `ObservationUnavailable`, `Deleting` |

Resource-specific diagnostic conditions use the stable vocabulary in
[status-contract.md](status-contract.md). Gateway conditions distinguish
tunnel, DNS, and membership observation; binding conditions distinguish current
node health; lease conditions distinguish gateway rules, delivery, and
application acknowledgement. The schema rejects those conditions on unrelated
kinds.

`Unknown` always uses `ObservationUnavailable`. `Ready=True` is resource-
specific live health; it is never inferred from registration or desired-state
publication. Status writes require current `observedGeneration`, preserve
transition timestamps when status does not change, and suppress semantic no-op
writes.

`Ready=True` has these exact resource-specific meanings:

| Kind | Required live observation |
| --- | --- |
| `VPNGatewayClass` | the declared controller, immutable release and conformance profile are available for new gateways |
| `VPNGateway` | its tunnel, routing and DNS path are live and continuously observed |
| `VPNEgressRoute` | its sole accepted parent is live and the route is eligible to program bindings; this does not assert that any Pod exists |
| `VPNWorkloadBinding` | deny-first state and the selected tunnel path are applied and live for the exact Pod UID and current node boot |
| `PortForwardLease` | the provider mapping, return path and exact SingleActive Pod UID are live after any handoff |
| `WorkloadAdapter` | the immutable adapter implementation is available and has passed its narrow protocol health check |

## Frozen scalar validation and defaults

Issue #128 must encode these rules in OpenAPI and object-local CEL without
weakening them:

| Field | Frozen rule |
| --- | --- |
| class `controllerName`, feature IDs and conformance profile | required qualified names; class must contain every frozen Core feature |
| class release version / manifest digest | non-empty immutable version and `sha256:` plus exactly 64 lowercase hexadecimal characters |
| class `parametersRef` | optional complete group/kind/name tuple; cluster-scoped and never `Secret` |
| gateway role refs | unique non-empty DNS-label role and local object name; native refs are `ConfigMap`, credential refs are `Secret` |
| gateway route consent | namespace mode enum `Same`, `Selector`, or `All`; `Selector` alone requires a selector |
| gateway cluster traffic / DNS | cluster mode is `BypassCluster` or `TunnelAll` and must be explicit; DNS mode defaults to the sole Core value `Gateway` |
| route parent | required explicit group `networking.waycloak.io`, kind `VPNGateway`, namespace and name; exactly one entry |
| binding identities | required non-empty Kubernetes UIDs, node name, opaque allocation identity and canonical address prefix; all controller-authored identity fields immutable |
| lease gateway | required explicit namespace and name |
| lease backend | group is exactly empty, kind is exactly `Service`, local name and typed name-or-number port required |
| lease protocol / endpoint policy | non-empty set drawn from `TCP`, `UDP`; policy defaults to and permits only `SingleActive` |
| adapter | full immutable OCI digest, protocol exactly `networking.waycloak.io/adapter/v1`, non-empty bounded application set |

No default selects a gateway, namespace, credential, backend, feature, ordinary
egress path, or alternate data-plane implementation.

## VPNGatewayClass

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNGatewayClass
metadata:
  name: gluetun.waycloak.io
spec:
  controllerName: gluetun.waycloak.io/controller
  parametersRef:                 # optional cluster-scoped, non-Secret input
    group: networking.waycloak.io
    kind: GluetunClassParameters
    name: bundled
  releaseIdentity:
    version: v1.0.0-beta.1
    manifestDigest: sha256:<64-lowercase-hex>
  supportedFeatures:
  - networking.waycloak.io/CoreFailClosedEgress
  - networking.waycloak.io/TCP
  - networking.waycloak.io/UDP
  - networking.waycloak.io/DNSContainment
  - networking.waycloak.io/GatewayReplacementRecovery
  - networking.waycloak.io/NodeRestartRecovery
  conformanceProfile: networking.waycloak.io/Core-v1
status:
  conditions: []
```

Every spec field is immutable. `manifestDigest` identifies the signed release
manifest that transitively pins images, charts, schemas and conformance evidence;
individual image fields do not enter public intent. `parametersRef` cannot name
a Secret. Unsupported classes remain visible with `Accepted=False`; another
controller does not claim them. Class `Ready=True` means the declared controller,
release and profile are observed available for gateway instantiation, not that a
tunnel exists.

## VPNGateway

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNGateway
metadata:
  name: proton
  namespace: network
spec:
  gatewayClassName: gluetun.waycloak.io
  nativeConfigRefs:              # same-namespace ConfigMaps, keyed by role
  - role: networking.waycloak.io/GluetunEnvironment
    name: proton-openvpn
  credentialRefs:                # same-namespace Secrets, keyed by role
  - role: networking.waycloak.io/OpenVPNCredentials
    name: proton-credentials
  requestedFeatures: []
  allowedRoutes:
    namespaces:
      from: Selector
      selector:
        matchLabels:
          networking.waycloak.io/gateway-access: allowed
  clusterTraffic:
    mode: BypassCluster
    bypassCIDRs:
    - 10.42.0.0/16
    - 10.43.0.0/16
  dns:
    mode: Gateway
  placement:
    nodeSelector: {}
    tolerations: []
status:
  gatewayClass:
    controllerName: gluetun.waycloak.io/controller
  supportedFeatures: []
  addresses: []
  conditions: []
```

Implementation images, container command lines, generated VNI and internal
filenames are not portable spec fields. Security-sensitive network defaults are
rendered or explicitly confirmed during bootstrap, not silently guessed by the
controller.

`gatewayClassName` is immutable. Native and credential reference lists are map
lists keyed by immutable qualified `role`, limited to 16 entries each. Native
references target only local ConfigMaps; credential references target only local
Secrets, whose values are mounted only into the gateway engine and are never
read into status. `requestedFeatures` and `bypassCIDRs` are bounded set lists.
`clusterTraffic.mode` is required and is `BypassCluster` or `TunnelAll`;
`BypassCluster` requires at least one explicitly reviewed CIDR. `dns.mode`
defaults to the sole Core value `Gateway`. `allowedRoutes.namespaces.from`
defaults to `Same` and supports `Same`, `Selector`, or explicit `All`; a selector
is required only for `Selector`. Namespace authorization labels must be managed
outside tenant authority. Tolerations are an atomic bounded list because their
ordering has no merge ownership contract.

## VPNEgressRoute

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: private
  namespace: media
spec:
  parentRefs:
  - group: networking.waycloak.io
    kind: VPNGateway
    namespace: network
    name: proton
  requiredFeatures:
  - networking.waycloak.io/UDP
status:
  parents:
  - parentRef:
      group: networking.waycloak.io
      kind: VPNGateway
      namespace: network
      name: proton
    controllerName: gluetun.waycloak.io/controller
    conditions: []
```

Core initially permits exactly one effective parent. The list shape and keyed
status follow route conventions so deliberate future multi-parent behavior does
not require replacing status ownership. Multiple parents are rejected until a
separate failover/sharding contract is accepted.

`parentRefs` is a map list keyed by `group`, `kind`, `namespace`, and `name`,
with both minimum and maximum one. All four identity fields are required and
canonical. `requiredFeatures` is a bounded set. Because `parentRef` is an object
and Kubernetes list-map keys must be scalar, `status.parents` is a bounded atomic
list, not a map list. Each entry is logically keyed by the complete canonical
`parentRef` plus immutable qualified `controllerName`; duplicates are forbidden
by CEL. In Core the maximum of one gives the responsible route manager exclusive
ownership of the atomic list and matching summary status. A future multi-parent
feature must first define conflict-safe status ownership rather than changing
this bound silently.

The Pod template enrolls with:

```yaml
metadata:
  labels:
    networking.waycloak.io/egress-route: private
```

The value is a same-namespace route name. It cannot encode namespace, gateway,
backend or feature options. Old gateway/data-plane annotations are invalid in
the replacement installation.

## VPNWorkloadBinding

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNWorkloadBinding
metadata:
  name: pod-<uid-derived-stable-name>
  namespace: media
  ownerReferences:
  - apiVersion: v1
    kind: Pod
    name: downloader-abc
    uid: <exact-pod-uid>
spec:
  podRef:
    name: downloader-abc
    uid: <exact-pod-uid>
  routeRef:
    name: private
    uid: <exact-route-uid>
  gatewayRef:
    namespace: network
    name: proton
    uid: <exact-gateway-uid>
  nodeName: worker-1
  allocation:
    identity: <stable-opaque-id>
    address: 192.0.2.10/32
status:
  appliedGeneration: 7
  observedPodUID: <exact-pod-uid>
  observedGatewayUID: <exact-gateway-uid>
  agent:
    nodeName: worker-1
    nodeBootID: <opaque-id>
    instanceID: <opaque-id>
    observedAt: 2026-07-26T00:00:00Z
  conditions: []
```

Only Waycloak service accounts may create or mutate bindings. Admission rejects
user attempts. Allocation identity is persisted before CNI success and is never
derived from list order. Status reports exact node-agent observation; object
existence is not readiness.

The gateway controller publishes the explicitly configured pool as the typed
status address `networking.waycloak.io/OverlayCIDR`. The binding controller
atomically reserves a canonical address with a non-expiring, gateway-owned
`coordination.k8s.io/Lease` before creating the binding. That internal Lease
contains only exact UIDs, opaque identity, address, and `Active` or
`Quarantined` state. It is not a workload projection or CNI handshake, and the
node agent cannot read it. Initial Core supports IPv4 `/16` through `/29` pools
and reserves the network address, first gateway host, and broadcast address.

Every reference and `nodeName` is immutable and UID-bound. Allocation identity
is immutable; an address may change only through controller-owned desired state,
which increments metadata generation. `appliedGeneration` reports the exact
generation authenticated by the local agent. The node agent has no Kubernetes
credential and never writes status directly. The binding controller relays its
authenticated observation. The exact same-namespace Pod owns the binding. A
`networking.waycloak.io/dataplane-cleanup` finalizer is limited to external node
and gateway withdrawal for at most ten minutes; timeout retains deny, never
reuses identity, quarantines the address, records `Ready=False`, and releases the
finalizer according to the #132 durable quarantine protocol.
ADR 0037 defines the atomic reservation, restart recovery, collision,
exhaustion, verified release, and missing-record quarantine behavior.

## PortForwardLease direction

The stable Extended API should target a typed Service backend rather than a raw
selector:

```yaml
spec:
  gatewayRef:
    namespace: network
    name: proton
  backendRef:
    group: ""
    kind: Service
    name: qbittorrent
    port: web
  endpointPolicy: SingleActive
  protocols: [TCP, UDP]
  applicationAdapterRef:
    name: qbittorrent
```

`SingleActive` needs explicit readiness, deterministic endpoint choice, drain,
handoff, UID identity and return-path proof. Until those semantics pass E2E
tests, the field is Experimental or the Extended feature remains unavailable.

The Extended schema is nevertheless fixed: `backendRef` targets a same-namespace
core `Service` and a typed name or number port; cross-namespace backends are not
accepted. `protocols` is a non-empty TCP/UDP set. `endpointPolicy` defaults to
and initially permits only `SingleActive`. The optional adapter reference is
same namespace. Status records Service UID, EndpointSlice UID, exact Pod UID,
handoff phase/generation and time-bounded provider observation. A bounded
`networking.waycloak.io/provider-cleanup` finalizer gets ten minutes to withdraw
the external mapping; timeout quarantines it and keeps `Ready=False`. #137 must
prove drain, handoff, return-path symmetry and no cross-delivery before the
Extended feature can be advertised.

## WorkloadAdapter direction

`WorkloadAdapter` remains an operator-authored trust record: immutable image
digest, supported application protocol/capability, and least-privilege delivery
contract. The adapter receives lease data through a narrow renewable protocol,
not Kubernetes or VPN credentials.

The resource is namespaced so a namespace operator can authorize only local
leases. `spec.image` is a full immutable OCI digest reference;
`spec.protocolVersion` is initially exactly
`networking.waycloak.io/adapter/v1`; supported applications and features are
bounded sets. All spec fields are immutable. The adapter process is out of
process and unprivileged and receives neither Kubernetes nor VPN credentials.
The trust record has no owner reference or finalizer.

## Reference authorization and privacy

| Referrer field | Target/scope | Consent and revocation | Privacy |
| --- | --- | --- | --- |
| class `parametersRef` | cluster-scoped non-Secret implementation parameters | distribution owns both; class/gateways withdraw if unresolved | no credential kind or value |
| gateway `gatewayClassName` | cluster `VPNGatewayClass` | cluster-visible class; deletion withdraws gateway/routes | class identity is public |
| gateway native/credential refs | local ConfigMap/Secret | namespace RBAC; deletion withdraws gateway | status reports role, never values |
| route `parentRefs` | local or remote `VPNGateway` | gateway `allowedRoutes` must admit route namespace; change immediately requeues and withdraws | before consent, `RefNotPermitted` reveals no existence |
| binding Pod/route/gateway refs | exact UID identities chosen by controller | enrollment plus already-proven route consent; deletion withdraws | binding exposes diagnostic identity only |
| lease gateway ref | local or remote `VPNGateway` | same gateway-side namespace consent as routes | same non-disclosure rule |
| lease backend/adapter refs | local Service/WorkloadAdapter | same-namespace RBAC; deletion drains then withdraws | only selected Pod UID enters status |

Core does not install or watch upstream `ReferenceGrant`. Its only public Core
cross-namespace relation is route-to-gateway, for which the gateway owner is the
correct consent authority and `allowedRoutes` supplies one complete handshake.
All other Core references are local or cluster-scoped. Extended Service and
adapter refs are local. A future new cross-namespace reference must either adopt
upstream `ReferenceGrant` as an explicitly versioned optional dependency or add
no such feature; Waycloak will not invent a temporary grant kind.

## Validation split

| Rule | Mechanism |
| --- | --- |
| field shape, enum, bounds, mutual exclusion | structural OpenAPI + CEL |
| immutable controller name / identity fields | CEL transition rules |
| local route-label syntax | label grammar + CEL/admission policy |
| target existence/capability/consent | controller reconciliation; not admission |
| user cannot author bindings | RBAC plus stable ValidatingAdmissionPolicy defense |
| node/CNI capability | observed node capability and CNI refusal |
| runtime health | node/gateway observation in status |

Admission success never substitutes for CNI or runtime readiness.

Core has no admission webhook. On Kubernetes 1.36, a stable
`MutatingAdmissionPolicy` adds only required CNI-capable-node scheduling metadata
to explicitly enrolled Pods. A stable `ValidatingAdmissionPolicy` rejects alpha
markers and non-controller binding writes. Both fail closed for their matched
objects. The chained CNI still resolves the label, binding and exact UID and
refuses unsafe setup independently. Dynamic reference checks remain asynchronous
controller work so controller loss cannot turn admission success into egress.

## Server-side field ownership

| Manager | Exclusive fields |
| --- | --- |
| `waycloak-class-controller` | `VPNGatewayClass.status` |
| `waycloak-gateway-controller` | `VPNGateway.status` and generated dependent specs it creates |
| `waycloak-route-<controller-name-hash>` | the Core route's sole-parent atomic status list plus matching route summary status |
| `waycloak-binding-controller` | binding metadata/spec/finalizer/status |
| `waycloak-lease-controller` | lease status and provider-cleanup finalizer |
| `waycloak-adapter-controller` | adapter status |

Controllers never apply user spec fields and never use force to take them.
Status managers use the status subresource and apply only their declared fields.
They retry conflicts with a fresh read and issue no write for semantic no-ops.
The route manager owns the whole bounded atomic `status.parents` list in Core;
condition lists remain map lists owned entry-by-entry. The node agent has no
field manager because it has no Kubernetes write credential.

## Deletion and failure semantics

- Deleting a route withdraws all associated bindings and traffic allowance
  through binding/gateway watches and bounded programming leases; existing Pods
  fail closed. The route has no finalizer because admission is not the packet
  boundary and user intent must not be held indefinitely.
- Deleting or rejecting a gateway withdraws route programming and bindings but
  does not delete user-owned routes. The gateway owns only same-namespace
  generated dependents and has no provider finalizer; leases own mappings.
- Deleting a Pod garbage-collects its same-namespace binding and idempotently
  starts its bounded binding finalizer, then garbage-collects it after observed
  withdrawal or the durable quarantine outcome.
- Node or agent loss sets observation Unknown/False and retains deny state.
- Finalizers exist only for external/provider cleanup; timeout produces an
  explicit quarantine/manual-resolution state, never silent reuse.

Class, route, lease, and adapter objects are never controller-owned user intent.
Cross-namespace owner references are forbidden. Class deletion leaves gateways
unresolved; gateway deletion leaves routes unresolved; neither cascades user
objects. `WorkloadAdapter` has no finalizer. Normal uninstall leaves all user
resources and CRDs; destructive purge remains the separately confirmed #139
operation.

## Alpha disposition

| Alpha contract | Treatment |
| --- | --- |
| gateway annotation | removed; old manifest is invalid |
| inline gateway engine image/type | removed; author a new class-backed gateway |
| `VPNWorkload` | deleted; never imported |
| allocation ConfigMap handshake | deleted; never imported |
| injected init/sidecar | removed |
| data-plane preview annotation | removed |
| selector lease target | removed; author new typed backend intent after support exists |

There is no field mapping, object translator, conversion webhook, compatibility
mode, or guarantee that an alpha concept has a replacement. The table records
deletion boundaries only.

## Review closure

- Group/version and minimum Kubernetes are frozen above.
- #124 proved CNI identity, bounded wait, deny-first rollback and restart on the
  accepted feasibility matrix; stable publication starts at Kubernetes 1.36.
- #125 and ADR 0035 freeze node-agent authentication and Kubernetes read scope.
- Parent and status list identity, immutability, reference semantics, field
  managers, finalizers and admission split are frozen above and audited by CI.
- Core does not use `ReferenceGrant` and does not claim Gateway API conformance.
- Port forwarding remains Extended and cannot be advertised before #137 proof;
  its stable object shape and local reference boundary are frozen here.
- #126 freezes alpha inventory and #139 teardown input. No replacement schema
  contains an alpha compatibility field or runtime image.

Issue #128 may now generate the reviewed structural schemas without reopening a
security or lifecycle decision. Any divergence from the machine-readable freeze
requires an ADR amendment and renewed #127-level review before generation.
