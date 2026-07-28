# Waycloak Helm chart

This chart currently represents the #128 API installation plus the #129–#136
route enrollment, static admission, cross-namespace, stable status, UID-bound
allocation, node-agent, and gateway-class contract surfaces. It
installs only the six `networking.waycloak.io/v1beta1` CRDs, generated persona,
controller, and unbound read-only node-agent RBAC, the future controller
ServiceAccount identity, controller-only `VPNWorkloadBinding` defense, and
declarative rejection of alpha Pod annotations, malformed route lookup labels,
live-Pod enrollment mutation, host-namespace/direct-node bypass, and a stable
declarative scheduling mutation for authenticated Core-ready nodes.

When `defaultGatewayClass.enabled=true`, the chart renders the tested Gluetun
class only from an exact release version and `sha256` manifest digest supplied
by the verified install plan. The development defaults do not invent a release
identity. Gateway manifests contain no Waycloak image digest.

It does not render the alpha controller, mutation webhooks, sidecars, init
containers, allocation ConfigMaps, or alpha CRDs. It also does not yet render
the replacement controller Deployment or CNI installer. The digest-only
node-agent DaemonSet surface remains disabled until the signed install plan
supplies its TLS and artifact identity plus root-owned paths for the install
receipt, CNI binary, and active conflist. Those three exact files are mounted
read-only and hash-checked before Core readiness; the runtime agent never
writes the host CNI directories. Do not enroll workloads from this
intermediate chart; the stable turnkey journey is not complete at #136.

The only replacement enrollment key is the Pod-template label
`networking.waycloak.io/egress-route: <same-namespace-route-name>`. A present
label is fail-closed intent even when the route is not yet accepted or ready.
Removing or changing enrollment requires changing the workload template and
creating a new Pod; the admission policy rejects edits to an existing Pod.

On Kubernetes 1.36, a stable `MutatingAdmissionPolicy` adds the hard
`networking.waycloak.io.node-restriction.kubernetes.io/core-ready=true` node
selector to enrolled Pods while preserving all user scheduling constraints.
The authenticated controller publishes that protected label only for an exact
release and Core capability report and expires it after agent loss. Stable
support requires the NodeRestriction admission plugin; the CNI independently
fails closed if admission or a scheduling label is missing, stale, or bypassed.
The chart installs no mutating/validating webhook or admission TLS resources.

Cross-namespace gateway references use `VPNGateway.spec.allowedRoutes`; Core
does not install Gateway API `ReferenceGrant`. Namespace labels selected for
authorization must be operator controlled and outside tenant write authority.
See [`cross-namespace-consent.md`](../../docs/security/cross-namespace-consent.md)
before enabling a selector.

```sh
helm lint charts/waycloak
```

CRDs follow Helm's `crds/` lifecycle: they install before namespaced resources and are not deleted during uninstall.

Persona roles are intentionally unbound. Grant workload authorship in each
approved workload namespace with a namespaced `RoleBinding` to the
`waycloak-workload-owner` ClusterRole. Do not use a `ClusterRoleBinding`, which
would grant route and lease authorship in every namespace.

The controller has no Secret permission by default. In each approved gateway
namespace, bind the `waycloak-gateway-secret-reader` ClusterRole to the chart's
controller ServiceAccount with a namespaced `RoleBinding`. Never use a
`ClusterRoleBinding` for this credential-reader role.

Install exactly one Waycloak release per cluster. The CRDs, admission policy,
and fixed persona ClusterRoles are cluster-wide product identities and are not
safe for competing Helm release ownership.
