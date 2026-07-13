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

## Port-forward request

The simplest product experience should remain one workload marker. Port-forward behavior belongs to the selected gateway profile or an additional explicit annotation only when the workload needs it:

```yaml
metadata:
  annotations:
    networking.waycloak.io/gateway: private-egress/proton-eu
    networking.waycloak.io/port-forward: tcp,udp
```

The second annotation is reserved as optional convenience because many
protected workloads only need egress. It must not encode provider-specific
details. The first Phase 4 control-plane slice requires an explicit
`PortForwardLease`; admission does not derive one from this annotation yet.

For the initial `ProtonNatPmp` path, use Proton/OpenVPN credentials whose
username already contains Proton's `+pmp` suffix. Waycloak mounts that Secret
only in Gluetun, disables Gluetun's own port-forward loop, and reports provider
acquisition separately from the still-pending gateway-rule and delivery
conditions. A public port in status is not yet proof of inbound reachability.

## KCL integration

KCL is an adapter over the Kubernetes contract:

```kcl
vpn = schema.VpnTrait {
    gateway = "private-egress/proton-eu"
    portForward = "tcp,udp"
}
```

The KCL module renders the same annotations and optionally validates known gateway names. The Waycloak controller must not know that KCL exists.

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

Ordinary listeners stay on the fixed `PortForwardLease.spec.target.port` while the gateway translates each provider public-port generation to that target. Applications may also advertise a peer port inside their own protocol; for those workloads Waycloak first tries a Pod-local standard such as NAT-PMP, PCP, or UPnP.

qBitTorrent 5.2.3 is an evidence-backed exception. In the Phase 4 compatibility probe it accepted a PCP mapping from local port `6881` to external port `42000`, but its real HTTP tracker request still announced `port=6881`. The official integration therefore requires a narrow qBitTorrent sidecar that consumes only the neutral lease record and changes the application listener. It remains outside the controller, receives no Kubernetes or VPN credentials, and must acknowledge the exact lease generation before application delivery is considered observed.

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
