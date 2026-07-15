# Waycloak KCL module

This optional module exposes KCL schemas generated from Waycloak's served CRDs
and constants for the canonical workload annotations. It is an authoring
adapter over the Kubernetes API; the Waycloak runtime never depends on KCL.

Add the released module from GHCR using the release version:

```sh
kcl mod add oci://ghcr.io/amoenus/waycloak-kcl --tag RELEASE_VERSION
```

Import the generated `v1alpha1` schemas and the annotation helpers:

```kcl
import waycloak.helpers
import waycloak.v1alpha1 as networking

gateway_ref = helpers.GatewayReference {
    namespace = "private-egress"
    name = "private"
}

gateway = networking.VPNGateway {
    metadata = {name = gateway_ref.name, namespace = gateway_ref.namespace}
    spec = {
        # Supply the ordinary Kubernetes API fields documented by Waycloak.
    }
}

pod_template_annotations = {
    helpers.gatewayAnnotation = gateway_ref.value
}
```

`VPNWorkload` is included because it is a served, inspectable API, but it is
controller-owned and consumers must not author it. The module contains no
credentials, private endpoints, provider defaults, or homelab-specific values.

Start with the egress-only example:

```sh
kcl run examples/private-egress.k -S items
```

It renders a native Gluetun ConfigMap plus one `VPNGateway` and exposes the
annotation map to merge into a workload Pod template. Provision the referenced
engine-only Secret separately. The more advanced `examples/basic.k` is the
Proton-specific example and adds a provider-assigned `PortForwardLease`.
