# Waycloak

Waycloak provides selected, fail-closed VPN egress for explicitly enrolled
Kubernetes workloads. Applications use ordinary Kubernetes Pod templates; VPN
credentials and the VPN engine stay in a shared gateway rather than in the
application Pod.

The current stable release is **v1.0.1**. It supports the
`networking.waycloak.io/v1beta1` API on the certified K3s/Flannel environment
described in the [getting started guide](docs/getting-started.md#supported-environment).
Do not deploy `v1.0.0`: its runtime artifacts did not pass the release
vulnerability gate.

## Start here

- [Getting started](docs/getting-started.md) — verify, install, create a
  gateway, and protect a workload.
- [Configuration reference](docs/configuration.md) — all operator-controlled
  installation, gateway, route, enrollment, RBAC, and optional feature settings.
- [Advanced setup](docs/advanced-setup.md) — cross-namespace routing, GitOps,
  port forwarding, observability, upgrades, and recovery.
- [Helm and OCI guide](docs/guides/helm.md) — inspect and render the chart while
  preserving the supported release lifecycle.
- [KCL guide](docs/guides/kcl.md) — author typed Waycloak resources with the
  optional OCI module.
- [API reference](docs/api/v1beta1.md) — generated field-level API details.

## The workload contract

Enrollment is explicit on a workload's Pod template:

```yaml
spec:
  template:
    metadata:
      labels:
        networking.waycloak.io/egress-route: private
```

That label is fail-closed intent. If the selected route, gateway, tunnel, DNS
path, node agent, or release identity is not healthy, Waycloak denies startup
or keeps egress blocked. It never silently falls back to ordinary internet
egress. Application containers receive no additional Linux capabilities.

Helm is the primary packaging surface and KCL is an optional authoring surface.
Initial installation, upgrades, and rollbacks use a verified, reviewable
`waycloakctl install plan/apply` transaction because the lifecycle also owns
the CNI chain, immutable release identity, observation trust, and fail-closed
activation sequence.

Waycloak does not claim anonymity. Its guarantee is selected, fail-closed VPN
egress within the documented [threat model](docs/security/threat-model.md).

Waycloak is licensed under the [MIT License](LICENSE). Apache-2.0 material
retains its required notices.
