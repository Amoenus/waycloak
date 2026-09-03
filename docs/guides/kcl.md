# KCL authoring guide

KCL is an optional typed authoring layer for Waycloak resources. It does not
install Waycloak and has no runtime role. Install the exact release through the
verified Helm/CLI transaction first.

After that one-time platform step, a GitOps repository may use KCL for routine
gateway, route, and workload changes without invoking `waycloakctl`. For the
minimal shared-gateway workflow, see [GitOps workload onboarding](gitops-workloads.md).

Supporting releases also include `examples/gitops-bootstrap-values.k`, which
renders the cluster-owned overlay for the canonical Helm chart. KCL remains an
optional authoring layer; users may feed its YAML to plain Helm, Flux, or Argo
CD as described in [GitOps-native platform bootstrap](gitops-bootstrap.md).

The stable OCI module is:

```text
oci://ghcr.io/amoenus/waycloak-kcl:1.0.1
sha256:4ccad6c38eac0028fe9aa7466b1cf7bce664f1cc25db5365288245361ebac45b
```

Verify the digest against `release-manifest.json` or `waycloak-kcl.ref`.

## Add the module

From an existing KCL package:

```sh
kcl mod add oci://ghcr.io/amoenus/waycloak-kcl --tag 1.0.1
```

This adds the dependency as `waycloak`. Import the generated v1beta1 schemas:

```python
import waycloak.v1beta1 as networking
```

The package includes schemas for `VPNGatewayClass`, `VPNGateway`,
`VPNEgressRoute`, `VPNWorkloadBinding`, `PortForwardLease`, and
`WorkloadAdapter`. `VPNWorkloadBinding` is available for validation and tooling
but is controller-authored, not user intent.

## Route and workload example

Create `main.k`:

```python
import waycloak.v1beta1 as networking

route = networking.VPNEgressRoute {
    metadata = {
        name = "private"
        namespace = "media"
    }
    spec = {
        parentRefs = [{
            group = "networking.waycloak.io"
            kind = "VPNGateway"
            namespace = "media"
            name = "private"
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
            metadata = {
                labels = {
                    app = "example"
                    "networking.waycloak.io/egress-route" = "private"
                }
            }
            spec = {
                containers = [{
                    name = "example"
                    image = "registry.example.invalid/example@sha256:replace-with-reviewed-digest"
                }]
            }
        }
    }
}

items = [route, workload]
```

Render, review, and apply:

```sh
kcl run -S items >waycloak-workload.yaml
kubectl apply -f waycloak-workload.yaml
```

Use an immutable workload image digest in real configuration. Credentials do
not belong in KCL source; create or synchronize the referenced Kubernetes
Secret from its credential source of truth.

## Repository example

The source tree includes `kcl/waycloak/examples/private-egress.k`. From the
module directory:

```sh
kcl run examples/private-egress.k -S items
```

Use that output only after the controller, CNI, gateway class, and target
gateway are healthy.

## Recommended repository layout

```text
platform/
  waycloak/
    kcl.mod
    kcl.mod.lock
    main.k
    environments/
      homelab.k
      production.k
```

Keep the module version aligned with the installed Waycloak release. Review
rendered YAML in CI, run policy checks against it, and let GitOps apply only
user-authored resources. Release transitions still use `waycloakctl` as
described in the [Helm guide](helm.md).

## Boundaries

- Do not author `VPNWorkloadBinding`, status, UIDs, finalizers, or allocations.
- Do not duplicate VPN credentials into KCL or protected workloads.
- Do not use KCL to bypass route consent or the install lifecycle.
- Preserve the exact Pod-template enrollment label; changing it requires new
  Pods.

See the [configuration reference](../configuration.md) for all resource fields
and [API reference](../api/v1beta1.md) for generated schema details. The
[workload-adapter guide](workload-adapters.md#kcl-trust-record) contains a
digest-pinned qBittorrent `WorkloadAdapter` example.
