# Cross-namespace gateway consent

Waycloak permits a `VPNEgressRoute` to reference a `VPNGateway` in another
namespace only when the gateway owner explicitly authorizes the route's
namespace through `VPNGateway.spec.allowedRoutes.namespaces`.

The default is `Same`. `Selector` matches labels on the source Namespace, and
`All` deliberately authorizes every namespace. The baseline does not install, read, or
watch Gateway API `ReferenceGrant`, and it defines no Waycloak-specific grant
kind. Gateway-side `allowedRoutes` is the complete baseline handshake.

## Label authority is a security boundary

Labels used by an authorization selector must be controlled by a trusted
cluster or network operator. A workload tenant that can add a matching label to
its own Namespace can authorize itself. The generated
`waycloak-workload-owner` role does not grant Namespace writes, but Waycloak
cannot compensate for broader RBAC granted elsewhere.

Before using `Selector`, verify the actual tenant identity cannot mutate the
Namespace:

```sh
kubectl auth can-i patch namespace workloads \
  --as=system:serviceaccount:workloads:tenant
```

The result must be `no`. Review admission policies and GitOps permissions as
well as RBAC. Prefer an operator-owned label prefix and audit every change.

## Privacy and failure behavior

Before consent, both an existing denied gateway and a nonexistent gateway
produce `ResolvedRefs=False` with reason `RefNotPermitted` and the same
non-sensitive message. Status and events do not expose target existence,
configuration, credentials, features, or runtime state.

The controller reads the gateway and source Namespace through an uncached API
reader. Namespace-label changes, gateway policy changes, gateway-class changes,
and gateway creation or deletion requeue dependent routes. Revoking consent
withdraws `Programmed` and `Ready`; an enrolled workload remains fail closed.
No cross-namespace owner reference is created.

Route and replacement `PortForwardLease` dependencies share exact gateway,
gateway-class, and source-Namespace index keys. The baseline route controller
consumes those mappings now. The optional replacement lease controller consumes
the lease mappings in #137; the alpha lease controller is intentionally not
connected to the replacement authorization path.

Changing ordinary egress behavior still requires removing the enrollment label
from the workload template and creating a new Pod. Consent revocation never
turns an already enrolled Pod into ordinary egress.

See `config/samples/networking_v1beta1_cross_namespace.yaml` for the authored-
from-scratch object relationship. Do not apply it until the complete baseline CNI,
node agent, binding, gateway, and controller path is installed.
