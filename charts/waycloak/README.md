# Waycloak Helm chart

This chart currently represents the #128 clean-break API installation slice. It
installs only the six `networking.waycloak.io/v1beta1` CRDs, generated persona,
controller, and unbound read-only node-agent RBAC, the future controller
ServiceAccount identity, and stable
validating-admission-policy defense for controller-only
`VPNWorkloadBinding` objects.

It does not render the alpha controller, mutation webhooks, sidecars, init
containers, allocation ConfigMaps, or alpha CRDs. It also does not yet render a
replacement controller, CNI plugin, or node agent. Do not enroll workloads from
this intermediate chart. #129-#136 add those components only after their hard
prerequisites pass; the stable turnkey journey is not complete at #128.

```sh
helm lint charts/waycloak
```

CRDs follow Helm's `crds/` lifecycle: they install before namespaced resources and are not deleted during uninstall.

Persona roles are intentionally unbound. Grant workload authorship in each
approved workload namespace with a namespaced `RoleBinding` to the
`waycloak-workload-owner` ClusterRole. Do not use a `ClusterRoleBinding`, which
would grant route and lease authorship in every namespace.
