# GitOps workload onboarding

If Waycloak is already installed and your platform team provides a shared
gateway, you do not need `waycloakctl` to protect an application. Commit one
namespaced `VPNEgressRoute` and add one label to the application's Pod template.
Argo CD, Flux, or another Kubernetes reconciler can apply both as ordinary
declarative resources.

This is the normal workload-author experience. Supporting releases also offer
a [GitOps-native clean platform bootstrap](gitops-bootstrap.md). The CLI remains
required for upgrade, rollback, rotation, and repair transactions; it is not
required for routine route or workload changes.

## What the platform team provides once

Before a workload owner uses this guide, the platform team must provide:

- a healthy Waycloak installation;
- a ready `VPNGateway`, such as `network-egress/shared`;
- gateway `allowedRoutes` consent for the workload namespace; and
- workload-owner RBAC in that namespace.

The shared gateway owns the VPN engine and references its credentials. Do not
copy VPN credentials into the application namespace or application Pod.

## The two Git changes

Assume the application is in namespace `media` and the platform gateway is
`network-egress/shared`.

First, add `waycloak-route.yaml` beside the application's other manifests:

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
      namespace: network-egress
      name: shared
  requiredFeatures:
    - networking.waycloak.io/TCP
    - networking.waycloak.io/UDP
    - networking.waycloak.io/DNSContainment
```

Second, add the route name to the controller-owned Pod template in the existing
Deployment, StatefulSet, Job, or other workload manifest:

```yaml
spec:
  template:
    metadata:
      labels:
        networking.waycloak.io/egress-route: private
```

For example, a small Kustomize application can contain:

```text
apps/media/qbittorrent/
  deployment.yaml          # Pod template contains the enrollment label
  service.yaml
  waycloak-route.yaml      # the route above
  kustomization.yaml
```

List the route with the existing resources:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - service.yaml
  - waycloak-route.yaml
```

Commit and let the reconciler apply the directory. Enrollment changes create a
new Pod; do not add or remove the label directly on a live Pod.

## The same change with KCL

KCL is an optional typed authoring layer for the same Kubernetes objects. A KCL
package can emit the route and labeled workload together:

```python
import waycloak.v1beta1 as networking

route = networking.VPNEgressRoute {
    metadata = {name = "private", namespace = "media"}
    spec = {
        parentRefs = [{
            group = "networking.waycloak.io"
            kind = "VPNGateway"
            namespace = "network-egress"
            name = "shared"
        }]
        requiredFeatures = [
            "networking.waycloak.io/TCP"
            "networking.waycloak.io/UDP"
            "networking.waycloak.io/DNSContainment"
        ]
    }
}

workload = {
    apiVersion = "apps/v1"
    kind = "Deployment"
    metadata = {name = "example", namespace = "media"}
    spec = {
        replicas = 1
        selector = {matchLabels = {app = "example"}}
        template = {
            metadata = {labels = {
                app = "example"
                "networking.waycloak.io/egress-route" = "private"
            }}
            spec = {containers = [{
                name = "example"
                image = "registry.example.invalid/example@sha256:replace-with-reviewed-digest"
            }]}
        }
    }
}

items = [route, workload]
```

Pin the Waycloak module to the installed release, render it in CI, and give the
rendered resources to GitOps:

```sh
kcl run -S items >waycloak-workload.yaml
```

See the [KCL authoring guide](kcl.md) for module installation and digest
verification. KCL does not install Waycloak and has no runtime role.

## Verify without the CLI

Kubernetes conditions and controller-created bindings are sufficient for the
workload-owner path:

```sh
kubectl -n media get vpnegressroute private
kubectl -n media describe vpnegressroute private
kubectl -n media get vpnworkloadbinding
```

Wait for the route's current `Accepted`, `ResolvedRefs`, `Programmed`, and
`Ready` conditions. `Ready=True` describes observed data-plane health. If the
route, gateway, tunnel, DNS path, or node agent is unhealthy, the enrolled Pod
stays blocked instead of falling back to ordinary egress.

`waycloakctl doctor` is an optional convenience for this verification; it is
not part of the declarative workload contract.

## When there is no shared gateway

A namespace-local gateway is still GitOps-managed after platform installation.
Commit the following non-secret resources:

- a namespaced `RoleBinding` granting the controller credential-reader access;
- a provider-native ConfigMap;
- a `VPNGateway` that references the ConfigMap and credential Secret;
- the `VPNEgressRoute`; and
- the workload Pod-template label.

Create the Secret through SOPS, External Secrets, Sealed Secrets, or another
credential controller appropriate to your cluster. Waycloak does not require
one of those products and is not responsible for configuring it. Never commit
the credential value itself.

`waycloakctl gateway init` may generate the ConfigMap and `VPNGateway` for the
qualified Proton/OpenVPN baseline, but those are normal YAML resources and may
instead be authored directly from the [configuration reference](../configuration.md#gateway-configuration).

## Platform lifecycle boundary

Initial platform installation and release transitions are different from
workload onboarding. They modify the host CNI chain and bind an exact release,
runtime image set, immutable gateway class, and observation trust to the live
cluster. In `v1.0.1`, ordinary continuous GitOps reconciliation is not the
transition authority for those operations; use the reviewed
`waycloakctl install plan/apply` flow described in the
[Helm and OCI guide](helm.md#supported-installation-flow).

Git may still record the pinned chart digest and reviewed plan-derived values.
This lifecycle boundary does not apply to gateways, routes, leases, adapters,
or workload templates, which remain normal declarative GitOps resources.
