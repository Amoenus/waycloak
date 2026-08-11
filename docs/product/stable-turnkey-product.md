# Stable and turnkey Waycloak product requirements

Status: Requirements accepted; replacement API frozen by issue #127
Last updated: 2026-08-11
Target: clean-break replacement architecture, then `v1` after beta evidence

## Product outcome

Waycloak becomes the dependable Kubernetes-native way to place any compatible
workload behind a selected VPN with minimal, explicit setup and no silent path
back to ordinary internet egress.

The application contract is one typed route plus one Pod-template enrollment
label. A platform owner may pre-provision the route, leaving one line for each
workload:

```yaml
networking.waycloak.io/egress-route: private
```

The label names a same-namespace `VPNEgressRoute`; the route is the canonical
API and holds the gateway reference, authorization, desired behavior and status.
No custom annotation is part of the replacement workload API.

Turnkey means safe defaults, preflight, reviewable generated manifests,
immutable artifacts, precise rejection, and self-explaining status. It never
means guessing trust boundaries, hiding privileged node requirements, copying
credentials, or weakening fail-closed behavior for compatibility.

## Product promise

For an explicitly enrolled non-privileged workload on a supported cluster,
Waycloak routes selected external Pod traffic through the accepted healthy VPN
gateway. It prevents the Pod sandbox from becoming runnable until deny-first
protection is installed, and it blocks traffic whenever the complete protected
path is not observed healthy. Waycloak does not claim anonymity.

## Personas and owned resources

| Persona                              | Owns                                                                                   | Must not own                                            |
| ------------------------------------ | -------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| Distribution/infrastructure provider | `VPNGatewayClass`, immutable images, schemas, conformance reports                      | account credentials or tenant route intent              |
| Cluster/network operator             | installation, `VPNGateway`, native input/Secret refs, route authorization, maintenance | application release manifests                           |
| Workload owner                       | `VPNEgressRoute`, Pod-template route label, optional `PortForwardLease`                | VPN credentials, overlay allocation or node programming |
| Waycloak controllers/agents          | `VPNWorkloadBinding`, dependents, status, kernel state                                 | user-authored desired resources                         |
| Application integrator               | optional adapter implementation                                                        | Kubernetes or VPN credentials                           |

## First-use journey

On a compatible cluster and supported provider, a new operator reaches a
verified protected curl workload within 15 minutes:

1. `waycloakctl preflight` identifies Kubernetes, runtime, chained-CNI,
   privilege, networking, DNS and architecture support without changing state.
2. The operator reviews generated Helm values, security requirements and
   rollback/cutover plan.
3. Helm installs a signed release, default `VPNGatewayClass`, CNI plugin, node
   agent and controller.
4. The operator creates a gateway from a tested provider recipe and Secret
   reference; no Waycloak image/VNI/webhook certificate knowledge is required.
5. The workload owner creates a route and adds the route label to the Pod
   template—or selects an operator-provisioned route with only the label.
6. The route and gateway converge through `Accepted`, `ResolvedRefs`,
   `Programmed`, and `Ready`; each Pod has a UID-bound binding.
7. An isolated smoke test proves VPN egress, DNS containment, ordinary workload
   control behavior, and fail-closed tunnel loss.

## Functional requirements

### ST-FR-1: Coherent typed API

The replacement API includes only kinds with distinct actor, lifecycle and
status needs: `VPNGatewayClass`, `VPNGateway`, `VPNEgressRoute`,
`VPNWorkloadBinding`, `PortForwardLease`, and `WorkloadAdapter`. Controller loop
count never determines CRD count.

### ST-FR-2: Explicit typed attachment

`VPNEgressRoute` is canonical. The Pod-template label names exactly one route in
the Pod namespace. Missing, invalid, rejected or ambiguous routes fail closed.
No namespace default or selector implicitly opts a workload in. The alpha
gateway annotation is rejected after cutover.

### ST-FR-3: Creation-time enforcement

A chained CNI plugin and node agent establish protection before application
containers start. Application Pods receive no Waycloak sidecars, init
containers, capabilities, host paths or injected credentials. CNI `ADD` failure
prevents sandbox startup; runtime agent/gateway loss preserves denial.

### ST-FR-4: Role and reference ownership

Gateway classes identify implementations; gateways identify tunnel instances;
routes express application attachment; bindings express observed per-Pod state.
Cross-namespace references need target-side consent. Denials do not leak target
existence before authorization.

### ST-FR-5: Portable status

User-facing resources use `Accepted`, `ResolvedRefs`, `Programmed`, and `Ready`
with current `observedGeneration`. `Ready` means live end-to-end data-plane
health, not object creation or desired-state publication. Component conditions
identify tunnel, DNS, membership, node, lease and application failures.
`VPNWorkloadBinding Ready=True` additionally requires both a fresh exact
node-agent observation and a non-deleting referenced `VPNGateway` whose exact
UID matches, whose status observes its current generation, and whose current
`Ready` condition is `True`; any mismatch or regression withdraws binding
readiness.

### ST-FR-6: Capabilities and conformance

Class, gateway, node and release advertise stable feature identifiers. Required
unsupported behavior is rejected before programming. Baseline, optional and
Experimental conformance reports are tied to immutable release digests and an
exact Kubernetes/CNI/runtime/kernel/architecture/provider matrix.

### ST-FR-7: Gateway and provider abstraction

Provider-native configuration stays behind a gateway adapter boundary. The
default class pins tested release-owned images. Credentials are referenced by
the gateway and mounted only into its engine. Unsupported providers or features
never fall back to a generic unverified path.

### ST-FR-8: Safe inbound forwarding

Port-forward provider mapping, gateway rules, backend identity, renewable
delivery and application acknowledgement remain separate observed states. The
stable target model should prefer a typed Kubernetes `Service` backend with an
explicit single-active endpoint/handoff contract; no selector may choose a Pod
nondeterministically.

### ST-FR-9: Turnkey lifecycle

`waycloakctl` is a signed stateless assistant for preflight, plan, install,
doctor, smoke test and support bundle. Plain Kubernetes remains
the source of truth and Helm the installation surface. Destructive or
privilege-expanding operations require explicit confirmation.

An exact Waycloak release transition may update the singleton gateway template
only through the confirmation-bound, journaled lifecycle transaction. The
gateway has no persistent volume; its StatefulSet is only a deterministic
singleton and `OnDelete` rollout-control primitive. After the chart runtime
converges, the same transaction inventories every exact UID-owned gateway,
deletes only a stale Pod with a UID precondition, and waits for the replacement
to run the target images and regain current live readiness. An interrupted
transaction resumes that ordered activation from its immutable journal. The
fail-closed path remains installed throughout, and transition completion
requires route/binding recovery with no ordinary-egress fallback.
The controller binds every gateway Pod template to the immutable release version
and manifest digest with controller-owned runtime annotations. They are observed
rollout evidence, not workload configuration, compatibility aliases, or an
annotation-based API. A release-identity change stages a distinct template even
when one or both gateway binary digests are unchanged.

### ST-FR-10: API and CRD lifecycle

Alpha replacement is a documented destructive reinstall, not migration or
conversion.
After beta, schemas, defaults, list semantics, field managers, finalizers,
references, conditions and deletion are compatibility-tested. CRD bundles are
installed/upgraded outside runtime reconciliation; purge is separate from
uninstall.

### ST-FR-11: Standards integration

Use Kubernetes extension points according to semantic fit: CRDs/controllers for
declarative intent, OpenAPI/CEL for local validation, typed conditions and
references, CNI for creation-time networking, Services/EndpointSlices for
workload identity where proven, and declarative admission policy for static
mutation on supported versions. Gateway API principles guide roles, routes,
parents, status and consent; Waycloak does not falsely claim Gateway API
conformance.

### ST-FR-12: Self-explaining operation

Conditions, events, metrics and `waycloakctl doctor` explain invalid intent,
unsupported nodes, reference denial, CNI refusal, tunnel/DNS loss, drift,
gateway replacement, lease renewal and adapter acknowledgement without Secret
or sensitive endpoint disclosure.

## Reliability and security requirements

- Zero direct-egress packets during startup, controller/admission/node-agent
  loss, tunnel/DNS/gateway loss, reconfiguration, upgrade, restore and cutover.
- UID-bound allocation and lease identities never use alphabetical order, Pod
  name reuse, or nondeterministic selectors.
- CNI `ADD`, `DEL`, `CHECK`, `GC`, rollback and runtime restart behavior is
  idempotent and chaos tested.
- Reconciliation is level based, no-op suppressing, conflict aware and bounded;
  finalizers cannot block deletion indefinitely without an observable quarantine
  escape procedure.
- Provider credentials stay only with the gateway engine; node agents and
  workloads receive none.
- Application containers gain no Linux capability, API token, host mount or
  implicit application credential from Waycloak.
- Tenant-writable namespace labels cannot authorize cross-namespace access.
- Multi-day exact-artifact real-provider soak, destructive lifecycle, backup,
  restore and gateway replacement tests gate stable release.

## API quality requirements

- Structural schemas reject unknown fields; object-local invariants use CEL.
- Every list declares atomic/set/map semantics and every reference declares
  group/kind/namespace/name rules.
- Defaults are safe, deterministic and round-trip stable.
- Status reasons and feature names are versioned API; messages are diagnostic
  and non-sensitive.
- User resources never contain controller implementation images or generated
  allocation details.
- No API kind exists merely because a separate reconciler exists.
- Experimental fields cannot silently graduate with a stable kind.

## Stable-release acceptance

1. Clean-cluster installation and alpha cutover both pass documented drills.
2. The 15-minute first-use journey succeeds without source-code knowledge.
3. No alpha attachment annotation or sidecar compatibility path remains in the
   stable runtime.
4. Baseline conformance passes on every published support-matrix row.
5. Fresh install, patch/minor upgrade, rollback, backup/restore and uninstall
   preserve the fail-closed and identity contracts.
6. Generic curl and qBittorrent scenarios pass their real-provider contracts.
7. Gateway replacement and endpoint rotation recover without workload restart
   wherever the class claims that capability.
8. Docs, schemas, Helm, examples, release metadata and runtime report one
   coherent API and feature set.
9. SBOM, signature, provenance, reproducibility and vulnerability gates pass.
10. A multi-day soak has no leak, identity collision, stale readiness,
    unbounded write loop or unexplained recurring outage.

## Non-goals

- Anonymity claims or protection from trusted cluster/node/CNI/kernel actors.
- Privileged, host-network or malicious workloads.
- Transparently converting an already-running unprotected Pod in place.
- A weak fallback for clusters that prohibit chained CNI installation.
- Hard runtime dependencies on Gateway API, cert-manager, KCL, Crossplane,
  Argo CD, ESO, a service mesh or Prometheus Operator.
- Multiple stable data planes maintained for legacy compatibility.
- Multi-gateway sharding, UI, or broad provider catalog before baseline proof.

## Normative design inputs

- [Product PRD](PRD.md)
- [Developer experience](developer-experience.md)
- [Stable API and Kubernetes alignment](../architecture/kubernetes-api-maturity.md)
- [Replacement API proposal](../api/replacement-api-proposal.md)
- [Threat model](../security/threat-model.md)
- [Stable implementation plan](../implementation/stable-product-plan.md)
- [Architecture decisions](../decisions/README.md)
