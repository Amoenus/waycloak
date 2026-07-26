# Stable API and Kubernetes alignment

Status: replacement target architecture
Last updated: 2026-07-26

Issue #127 freezes this target as `networking.waycloak.io/v1beta1` on
Kubernetes 1.36 or newer. The exact reviewed schema boundary is
[replacement-api-freeze.json](../api/replacement-api-freeze.json); issue #128 is
the first generated-API implementation step.

## Executive decision

Waycloak will use the remaining pre-stable compatibility window to replace its
annotation-and-sidecar architecture. The north star is a typed, role-oriented
Kubernetes API and a creation-time CNI security boundary:

```text
Distribution                     Cluster operator
     |                                  |
     v                                  v
VPNGatewayClass ----------------> VPNGateway
                                        ^ parentRef + consent
                                        |
Workload owner                    VPNEgressRoute
     |                                  ^
     +-- Pod template label ------------+
          egress-route: private
                       |
                       v
             VPNWorkloadBinding (controller-owned, Pod UID)
                       |
             chained CNI + node agent
                       |
                       v
            deny first -> VPN gateway -> internet
```

This follows Gateway API's durable ideas—personas, classes, parent references,
attachment consent, portable status and conformance—without reusing kinds whose
listener/traffic semantics do not match whole-Pod VPN egress.

## As-built assessment

The alpha design has strong invariants worth preserving:

- explicit workload opt-in;
- no silent backend or ordinary-egress fallback;
- persisted, UID-bound membership and port identity;
- observed data-plane readiness;
- credentials isolated to the gateway engine;
- provider and workload adapters behind interfaces; and
- backend-neutral packet conformance.

Its permanent API/runtime shape is not the target:

- a custom annotation is the entire workload attachment API;
- `VPNGateway` mixes implementation and instance ownership;
- admission injects privileged init/sidecar components into each Pod;
- a ConfigMap/projected-file handshake sits in the startup security path;
- the CNI/node architecture is framed as an optional preview;
- alpha compatibility and migration fields risk fossilizing; and
- CRD/install documentation is inconsistent about the four current kinds.

## Resource model and ownership

| Kind | Scope | Author | Purpose | Deletion relationship |
| --- | --- | --- | --- | --- |
| `VPNGatewayClass` | cluster | distribution/provider | implementation identity, defaults, features, conformance | never owned by controller Deployment |
| `VPNGateway` | namespace | cluster/network operator | tunnel instance, inputs, placement, route authorization | owns same-namespace generated gateway dependents |
| `VPNEgressRoute` | namespace | workload owner | typed gateway attachment and portable workload policy | independent user intent |
| `VPNWorkloadBinding` | namespace | controller | exact Pod UID allocation/applied/live observation | owned by exact Pod when scope permits |
| `PortForwardLease` | namespace | workload owner | renewable inbound intent to typed backend | independent, bounded external-cleanup finalizer |
| `WorkloadAdapter` | namespace | operator | immutable trusted adapter identity/capability | independent trust policy |

CRD count is a consequence of distinct ownership/lifecycle/status, never the
number of reconciler loops. User intent is never owned by the controller
Deployment. Cross-namespace owner references are never used.

## Workload attachment contract

The route is canonical:

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
```

The workload explicitly enrolls:

```yaml
spec:
  template:
    metadata:
      labels:
        networking.waycloak.io/egress-route: private
```

Kubernetes does not offer third parties a typed extension field inside
`PodSpec`. A same-namespace route-name label is therefore the narrow binding
key, not a configuration bag. It is selector/index friendly, visible in Git,
and cannot encode gateway implementation options. Missing or rejected routes
make CNI setup fail. Unlabeled Pods remain untouched.

## Creation-time and runtime lifecycle

```text
Pod object stored with route label
          |
          +--> controller resolves route/gateway and creates UID binding
          |
kubelet invokes primary CNI, then Waycloak chained CNI
          |
          +--> local agent verifies exact Pod UID and node capability
          +--> installs deny-first boundary
          +--> programs allocated route/overlay
          +--> verifies DNS and protected reachability
          |
          +--> success: sandbox may start
          `--> any failure: CNI ADD fails; sandbox is not runnable

runtime node agent continuously repairs drift and withdraws allow paths
when tunnel, gateway, membership, DNS, or local ownership is unhealthy
```

Admission may reject impossible intent and improve placement. It is not the
packet boundary. Stable MutatingAdmissionPolicy is used for static metadata only
when the supported Kubernetes version provides it; dynamic reference checks may
use a minimal webhook. CNI independently refuses unsafe setup.

## Reference and authorization model

Every reference passes four separate tests:

1. **Existence** — target is observed.
2. **Compatibility** — group, kind, class, protocol and features match.
3. **Consent** — target owner permits the relationship.
4. **Runtime usability** — target is programmed and healthy enough for the
   requested lifecycle stage.

`ResolvedRefs` summarizes the first three. `Programmed` and `Ready` summarize
applied and live state. A cross-namespace route-to-gateway reference requires
gateway-side allowed-route consent. Every other initial Core reference is local
or cluster-scoped, so Core has no Gateway API CRD dependency. A future different
cross-namespace reference requires a new review before adopting upstream
`ReferenceGrant`; Waycloak defines no temporary grant. Unauthorized status does
not reveal target existence before consent.

## Status and field ownership

```text
Accepted(generation)
        -> ResolvedRefs(generation)
        -> Programmed(generation)
        -> Ready(generation, current live health)
```

All conditions are `metav1.Condition`, positive polarity, semantically
no-op-suppressed, and carry current `observedGeneration`. `Unknown` means
observation is unavailable. Component conditions explain the packet path.
Route `status.parents[]` entries are keyed by parent reference and immutable
controller name. Desired, API-published, component-applied and live state are
never collapsed into one boolean.

## Standards fit

| Upstream surface | Waycloak use |
| --- | --- |
| CRDs/controllers | canonical declarative domain API and reconciliation |
| Gateway API | design vocabulary for roles, classes, parent refs, consent, status and conformance; no false conformance claim |
| CNI | mandatory creation-time network lifecycle and rollback boundary |
| OpenAPI/CEL | structural and object-local validation/default constraints |
| Mutating/ValidatingAdmissionPolicy | static, in-process policy on supported Kubernetes versions |
| Admission webhook | only dynamic checks/placement that cannot be expressed locally; never sole enforcement |
| Service/EndpointSlice | preferred future port-forward backend identity after single-active handoff proof |
| NetworkPolicy/AdminNetworkPolicy | defense in depth, not proof of the Pod-netns VPN invariant |
| Secret/ConfigMap | referenced gateway inputs and narrow generated projections; never credentials in workloads/status |
| owner refs/finalizers | same-scope lifecycle and bounded external cleanup only |
| server-side apply | explicit manager ownership, no takeover of user fields |

## Clean cutover philosophy

There is no stable compatibility obligation to the alpha API. The transition
uses a maintenance window, stopped protected workloads, old deny rules retained
until stop, alpha purge, fresh CRDs/components, newly authored intent, new
allocations and mandatory smoke tests. The old annotation, sidecars,
allocation ConfigMaps and alpha runtime objects do not remain dormant in the
stable controller.

After beta, Waycloak adopts normal Kubernetes compatibility discipline and
storage migration. The rewrite opportunity is consumed once.

## Turnkey distribution

The supported distribution consists of:

1. signed CRD bundle, Helm chart, images, SBOMs, provenance and one support-matrix
   manifest;
2. default tested gateway class and minimal provider recipes;
3. stateless `waycloakctl` for preflight, plan, install, doctor, conformance
   smoke tests and redacted support bundles; and
4. exact-matrix conformance and multi-day soak reports.

Plain Kubernetes remains the source of truth; Helm remains the installation
surface. The assistant never becomes a second database or hidden imperative
controller.

## Decision map

| Concern | Decision |
| --- | --- |
| Clean break and feature channels | [ADR 0025](../decisions/0025-api-stability-and-feature-channels.md) |
| Personas/classes | [ADR 0026](../decisions/0026-role-oriented-api-and-gateway-classes.md) |
| Typed route and enrollment | [ADR 0027](../decisions/0027-explicit-workload-opt-in-and-attachment.md) |
| Cross-namespace consent | [ADR 0028](../decisions/0028-reference-authorization-and-cross-namespace-consent.md) |
| Conditions/status | [ADR 0029](../decisions/0029-common-status-and-condition-contract.md) |
| Capabilities/conformance | [ADR 0030](../decisions/0030-capability-advertisement-and-conformance-profiles.md) |
| CRD/cutover lifecycle | [ADR 0031](../decisions/0031-crd-installation-conversion-and-storage-lifecycle.md) |
| Turnkey bootstrap | [ADR 0032](../decisions/0032-turnkey-bootstrap-and-preflight.md) |
| Upstream boundaries | [ADR 0033](../decisions/0033-upstream-api-integration-boundary.md) |
| CNI enforcement | [ADR 0034](../decisions/0034-cni-creation-time-enforcement.md) |

## Primary upstream references

- [Extending Kubernetes](https://kubernetes.io/docs/concepts/extend-kubernetes/)
- [Kubernetes custom resources](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)
- [Kubernetes CRD versioning](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/)
- [Kubernetes CEL](https://kubernetes.io/docs/reference/using-api/cel/)
- [Kubernetes MutatingAdmissionPolicy](https://kubernetes.io/docs/reference/access-authn-authz/mutating-admission-policy/)
- [Kubernetes labels](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/)
- [Gateway API roles and personas](https://gateway-api.sigs.k8s.io/docs/concepts/roles-and-personas/)
- [Gateway API conformance](https://gateway-api.sigs.k8s.io/docs/concepts/conformance/)
- [Gateway API ReferenceGrant](https://gateway-api.sigs.k8s.io/reference/api-types/referencegrant/)
- [Gateway API policy attachment](https://gateway-api.sigs.k8s.io/geps/gep-713/)
- [CNI specification](https://www.cni.dev/docs/spec/)
- [Helm CRD best practices](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/)
