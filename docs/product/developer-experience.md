# Developer experience

## Design principle

There is one source-of-truth marker on the workload Pod template. Higher-level tools translate their own syntax into that marker; they do not create parallel networking implementations.

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
  "state": "Active",
  "gateway": "private-egress/proton-eu",
  "publicPort": 52197,
  "protocols": ["TCP", "UDP"],
  "generation": 4,
  "renewAfter": "2026-07-13T12:30:00Z"
}
```

An application adapter can watch the file or local endpoint and call the application's API. Official examples should include qBitTorrent; Loadstone and Bitmagnet can consume the neutral contract directly where possible.

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
