# Replacement API proposal

Status: design sketch; schemas are not yet implemented
Last updated: 2026-07-26

## Goals

- Make typed resources the only source of Waycloak policy.
- Separate distribution, operator, application and controller ownership.
- Preserve explicit Pod-template enrollment without a custom annotation API.
- Make unsupported, unauthorized, unapplied and unhealthy states distinct.
- Allow one clean alpha cutover and a credible path to stable `v1`.

The examples use `networking.waycloak.io/v1beta1` as a working target. The
version is not published until schema and lifecycle reviews pass.

## Kinds at a glance

| Kind | Scope | Writable spec owner | Status owner | Core |
| --- | --- | --- | --- | --- |
| `VPNGatewayClass` | cluster | distribution/provider | class controller | yes |
| `VPNGateway` | namespace | cluster/network operator | gateway controller | yes |
| `VPNEgressRoute` | namespace | workload owner | route controller per parent | yes |
| `VPNWorkloadBinding` | namespace | controller only | controller/node observations | yes, internal-facing |
| `PortForwardLease` | namespace | workload owner | lease controller/manager observations | Extended |
| `WorkloadAdapter` | namespace | operator | adapter observer/controller | Extended |

## Common reference types

References use explicit `group`, `kind`, `name`, and optional `namespace`.
Defaults are allowed only when unambiguous and documented. Cross-namespace
references are never inferred. Namespaced local references default namespace to
the referring object namespace; cluster-scoped references omit it.

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

## VPNGatewayClass

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNGatewayClass
metadata:
  name: gluetun.waycloak.io
spec:
  controllerName: gluetun.waycloak.io/controller
  parametersRef:                 # optional implementation configuration
    group: networking.waycloak.io
    kind: GluetunClassParameters
    name: bundled
status:
  supportedFeatures:
  - CoreFailClosedEgress
  - TCP
  - UDP
  - DNSContainment
  - GatewayReplacementRecovery
  conditions: []
```

`controllerName` is immutable. Release-owned image digests live in a class
parameter or immutable release manifest, not every gateway. Unsupported classes
remain visible with `Accepted=False`; another controller does not claim them.

## VPNGateway

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNGateway
metadata:
  name: proton
  namespace: network
spec:
  gatewayClassName: gluetun.waycloak.io
  nativeConfigRefs:
  - kind: Secret
    name: proton-openvpn
  routeAccess:
    namespaces:
      from: Selector
      selector:
        matchLabels:
          networking.waycloak.io/gateway-access: allowed
  clusterTraffic:
    mode: Bypass
  dns:
    mode: Gateway
  placement: {}
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
  - UDP
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
  gatewayRef:
    namespace: network
    name: proton
  allocation:
    identity: <stable-opaque-id>
status:
  nodeName: worker-1
  appliedGeneration: 7
  conditions: []
```

Only Waycloak service accounts may create or mutate bindings. Admission rejects
user attempts. Allocation identity is persisted before CNI success and is never
derived from list order. Status reports exact node-agent observation; object
existence is not readiness.

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
  applicationAdapterRef:
    name: qbittorrent
```

`SingleActive` needs explicit readiness, deterministic endpoint choice, drain,
handoff, UID identity and return-path proof. Until those semantics pass E2E
tests, the field is Experimental or the Extended feature remains unavailable.

## WorkloadAdapter direction

`WorkloadAdapter` remains an operator-authored trust record: immutable image
digest, supported application protocol/capability, allowed lease namespaces,
and least-privilege delivery contract. The adapter receives lease data through
a narrow renewable protocol, not Kubernetes or VPN credentials.

## Validation split

| Rule | Mechanism |
| --- | --- |
| field shape, enum, bounds, mutual exclusion | structural OpenAPI + CEL |
| immutable controller name / identity fields | CEL transition rules |
| local route-label syntax | label grammar + CEL/admission policy |
| target existence/capability/consent | controller and minimal dynamic admission where useful |
| user cannot author bindings | RBAC plus validating admission defense |
| node/CNI capability | observed node capability and CNI refusal |
| runtime health | node/gateway observation in status |

Admission success never substitutes for CNI or runtime readiness.

## Deletion and failure semantics

- Deleting a route withdraws all associated bindings and traffic allowance
  before cleanup; existing Pods fail closed.
- Deleting or rejecting a gateway withdraws route programming and bindings but
  does not delete user-owned routes.
- Deleting a Pod garbage-collects its same-namespace binding and idempotently
  releases/quarantines allocation.
- Node or agent loss sets observation Unknown/False and retains deny state.
- Finalizers exist only for external/provider cleanup; timeout produces an
  explicit quarantine/manual-resolution state, never silent reuse.

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

## Open design gates

Before code generation:

1. settle exact group/version and Kubernetes minimum version;
2. prototype CNI identity, bounded binding wait and rollback on every target
   runtime/CNI;
3. specify node-agent local authentication and RBAC;
4. define route parent/status list map keys and CEL transition rules;
5. decide whether upstream `ReferenceGrant` is Core, optional or unnecessary;
6. prove Service/EndpointSlice single-active port-forward semantics;
7. perform threat-model update for node-wide privilege; and
8. freeze alpha stop/purge/reinstall procedure.
