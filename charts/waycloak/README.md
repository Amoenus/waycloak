# Waycloak Helm chart

This chart currently represents the #128 API installation plus the static #129
Pod-enrollment admission slice. It
installs only the six `networking.waycloak.io/v1beta1` CRDs, generated persona,
controller, and unbound read-only node-agent RBAC, the future controller
ServiceAccount identity, controller-only `VPNWorkloadBinding` defense, and
declarative rejection of alpha Pod annotations, malformed route lookup labels,
and live-Pod enrollment mutation.

It does not render the alpha controller, mutation webhooks, sidecars, init
containers, allocation ConfigMaps, or alpha CRDs. It also does not yet render a
replacement controller Deployment, CNI plugin, or node agent. Do not enroll
workloads from this intermediate chart. #129-#136 add those components only after their hard
prerequisites pass; the stable turnkey journey is not complete at #129.

The only replacement enrollment key is the Pod-template label
`networking.waycloak.io/egress-route: <same-namespace-route-name>`. A present
label is fail-closed intent even when the route is not yet accepted or ready.
Removing or changing enrollment requires changing the workload template and
creating a new Pod; the admission policy rejects edits to an existing Pod.

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
