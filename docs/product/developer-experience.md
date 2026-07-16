# Developer experience

## Design principle

There is one source-of-truth marker on the workload Pod template. Higher-level tools translate their own syntax into that marker; they do not create parallel networking implementations.

Waycloak is declaratively visible and operationally invisible. The gateway
annotation is the ordinary egress opt-in; a `PortForwardLease` is the additional
declaration for inbound reachability. Neither declaration should force an
application image to understand a VPN provider or a Waycloak API when normal
network translation can preserve its existing behavior.

## Plain Kubernetes

```yaml
spec:
  template:
    metadata:
      annotations:
        networking.waycloak.io/gateway: private-egress/proton-eu
```

The annotation value is `<namespace>/<name>`. The gateway authorizes consumer namespaces with `spec.workloadAccess.namespaceSelector`. A bare name is allowed only when the gateway is in the workload namespace.

Removing the annotation only affects new Pods. Deployment rollout behavior remains standard Kubernetes behavior and should be made clear by status/events and documentation.

Admission records
`internal.networking.waycloak.io/admission-generation` on each injected Pod.
This internal observation is the deterministic controller-and-agent generation
that performed the mutation; operators compare it with the Helm-owned desired
generation ConfigMap during upgrades. It is not a user input and must not be
copied onto workload templates.

## Port-forward request

The simplest product experience should remain one workload marker. Port-forward behavior belongs to the selected gateway profile or an additional explicit annotation only when the workload needs it:

```yaml
metadata:
  annotations:
    networking.waycloak.io/gateway: private-egress/private
    networking.waycloak.io/port-forward: tcp,udp
```

The second annotation is reserved as optional convenience because many
protected workloads only need egress. It must not encode provider-specific
details. The first Phase 4 control-plane slice requires an explicit
`PortForwardLease`; admission does not derive one from this annotation yet.

Provider port forwarding is a capability of the configured gateway, not part
of the workload API. Provider-specific setup belongs in a provider guide. A
public port in status is not proof of inbound reachability until gateway-rule,
target, and delivery observations are also ready.

## VPN engine configuration

Applications never configure the VPN engine. Platform operators configure the
engine using its native settings and reference credentials in the gateway
namespace. Waycloak reserves only the settings required to observe and enforce
the shared gateway contract.

For example, Gluetun remains responsible for provider, OpenVPN/WireGuard,
server-selection, custom-provider, DNS, and updater settings. Put those
non-secret native variables in referenced ConfigMaps and mount native
ConfigMap or Secret files only into the engine through `engine.config`.
Waycloak validates its small reserved integration boundary without copying
native values. The `v0.2` `provider` fields remain a mutually exclusive
migration surface; new gateways should use the engine-native shape defined by
[ADR 0017](../decisions/0017-engine-native-configuration-boundary.md).

Changing a referenced native ConfigMap changes the gateway Pod-template
digest and emits `GatewayRolloutRequired`; the singleton remains `OnDelete`, so
activate it during the documented fail-closed maintenance window. Secret
projection updates remain engine-only, but operators must restart the gateway
when the selected native engine does not reload that file. Migrate back to the
legacy `provider` shape before rolling a controller back below `v0.3.0`.

## KCL integration

KCL is an optional adapter over the Kubernetes contract. Each release
publishes `ghcr.io/amoenus/waycloak-kcl` separately from the primary Helm
installer. Verify its digest from the signed release manifest, add the matching
release tag, and commit the generated `kcl.mod.lock` digest:

```sh
kcl mod add oci://ghcr.io/amoenus/waycloak-kcl --tag 0.2.1
```

The module exposes CRD-generated schemas and the canonical annotation names:

```kcl
import waycloak.helpers
import waycloak.v1alpha1 as networking

gateway_ref = helpers.GatewayReference {
    namespace = "private-egress"
    name = "private"
}

pod_template_annotations = {
    helpers.gatewayAnnotation = gateway_ref.value
}
```

`networking.VPNGateway` and `networking.PortForwardLease` render the same API
objects as plain YAML. The generated `networking.VPNWorkload` schema is
inspectable but controller-owned and must not be authored. The controller does
not know that KCL exists, and the module contains no credentials, private
endpoints, or homelab defaults.

## Helm and Kustomize consumers

Consumers add annotations through their chart values or overlays. Waycloak publishes examples but does not require application charts to depend on the Waycloak chart.

## Feedback to developers

`kubectl describe pod` should answer:

- whether mutation occurred;
- which gateway was selected;
- which agent version was injected;
- whether initial fail-closed policy installed;
- whether the overlay reached the gateway;
- whether the gateway tunnel is healthy;
- whether public egress was verified;
- whether a forwarded port was requested and acquired.

Waycloak should also provide a read-only CLI or kubectl plugin eventually, but CRD status and events remain sufficient and canonical.

## Application integration for forwarded ports

Applications have different configuration models. Waycloak therefore exposes a stable neutral record rather than embedding qBitTorrent, Bitmagnet, or Loadstone logic in the controller.

Example mounted JSON:

```json
{
  "apiVersion": "networking.waycloak.io/v1alpha1",
  "podUID": "5c2d2a4e-63be-4b57-a27b-8d7803f9aa15",
  "leases": [{
    "identity": "90c8629c-f9cd-49a5-bc91-4471ce3e914e",
    "namespace": "downloads",
    "name": "torrent",
    "state": "Active",
    "gateway": "private-egress/proton-eu",
    "publicPort": 52197,
    "targetPort": 6881,
    "applicationPort": 6881,
    "applicationPortMode": "Fixed",
    "protocols": ["TCP", "UDP"],
    "generation": 4,
    "issuedAt": "2026-07-13T12:29:00Z",
    "renewAfter": "2026-07-13T12:29:45Z",
    "expiresAt": "2026-07-13T12:30:00Z"
  }]
}
```

Set `networking.waycloak.io/port-forward-container: <container>` on the opted-in Pod template to mount only this document at `/run/waycloak/port-forward/port-forward-leases.json` in that application container. The same record is available read-only at `http://127.0.0.1:9809/v1/port-forward/leases` and `/v1/port-forward/leases/<identity>`. The application container receives neither the rest of the allocation ConfigMap nor Kubernetes credentials or added capabilities.

An application adapter can watch the file or poll the local endpoint when it genuinely needs provider metadata. In the default `Fixed` mode, `Delivered=True` means the target Pod agent loaded the exact unexpired UID/generation record; it does not claim that an arbitrary application consumed it. In `ProviderAssigned` mode, the adapter must additionally acknowledge the exact generation and applied application port, and the agent must reconcile the corresponding local redirect before delivery becomes observed.

Application-specific adapters use the public
`networking.waycloak.io/adapter/v1alpha1` contract. Operators approve an exact
digest with a cluster-scoped `WorkloadAdapter`; workload authors select that
trust record and an explicitly authored sidecar with the
`networking.waycloak.io/workload-adapter` and
`networking.waycloak.io/adapter-container` Pod-template annotations. Waycloak
validates the selection but does not invent application configuration or mount
workload credentials automatically.

Ordinary listeners stay on the fixed `PortForwardLease.spec.target.port` while the gateway translates each provider public-port generation to that target. Applications may also advertise a peer port inside their own protocol; for those workloads Waycloak first tries a Pod-local standard such as NAT-PMP, PCP, or UPnP.

qBitTorrent 5.2.3 is an evidence-backed exception. In the Phase 4 compatibility probe it accepted a PCP mapping from local port `6881` to external port `42000`, but its real HTTP tracker request still announced `port=6881`. The official integration therefore requires a narrow qBitTorrent sidecar that consumes only the neutral lease record and changes the application listener. It remains outside the controller, receives no Kubernetes or VPN credentials, and must acknowledge the exact lease generation before application delivery is considered observed.

## Writing a workload adapter

An adapter is an ordinary unprivileged OCI sidecar implementing Waycloak's
versioned Pod-local lease protocol. It reads the neutral record, applies only
application-owned configuration, verifies the resulting application state,
and acknowledges the exact Pod UID, lease identity, generation, and applied
port. It does not call Kubernetes or receive VPN credentials or networking
capabilities.

Adapter readiness is generation-bound. Missing, expired, ambiguous, or changed
lease state; a missing application listener; and a rejected acknowledgement
withdraw readiness immediately. An adapter may retain readiness through a
bounded transient failure of an application control API only when the failure
concerns the exact lease identity, generation, and application port that it
most recently applied and verified. The reference qBitTorrent adapter permits
at most two consecutive failed observations and 15 seconds from the first
failure, whichever is reached first; a successful observation recovers
immediately. Adapters must log state transitions without logging every poll.

Waycloak will publish protocol schemas, fixtures, a black-box conformance
suite, OCI metadata requirements, and a minimal sample. Authors may use any
language; importing Waycloak's Go internals is not part of the contract. An
operator explicitly trusts and installs digest-pinned adapters before a
workload can select them. The complete extension contract is defined by
[ADR 0018](../decisions/0018-workload-adapter-protocol.md) and tracked by
[issue #67](https://github.com/Amoenus/waycloak/issues/67).

## Failure experience

A protected application may remain running while external connectivity is blocked. Kubernetes readiness should report the networking dependency where appropriate, but Waycloak must avoid restarting healthy application processes in a tight loop during provider outages.

Clear condition reasons are preferable to generic `NotReady`, including:

- `GatewayNotFound`
- `AdmissionVersionConflict`
- `SecurityPolicyRejected`
- `RouteSetupFailed`
- `GatewayUnreachable`
- `TunnelNotReady`
- `EgressVerificationFailed`
- `PortForwardUnsupported`
- `LeasePending`
- `LeaseActive`
- `LeaseDeliveryFailed`

## UX acceptance criteria

1. A fresh user protects an nginx/curl workload with one annotation.
2. No VPN provider settings appear in the workload manifest.
3. Removing the annotation and rolling the Deployment restores normal egress.
4. `kubectl` alone explains why a protected workload lacks connectivity.
5. The same workload manifest works whether it was authored directly, by Helm, Kustomize, or KCL.
