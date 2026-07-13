# Troubleshooting

Start with Kubernetes conditions and events; they use stable reasons and do not contain credential values.

```sh
kubectl describe vpngateway -n waycloak-egress example
kubectl get vpnworkload -A
kubectl get events -A --sort-by=.lastTimestamp
```

## An annotated Pod is rejected

- `GatewayNotFound`: check the `<namespace>/<name>` annotation.
- `GatewayUnauthorized`: label the workload namespace so it matches `spec.workloadAccess.namespaceSelector`.
- an injection-version error: roll the workload so a new Pod is admitted by the installed controller and immutable agent image.
- webhook TLS or availability error: check both controller replicas, the webhook Service endpoints, serving-certificate SAN, expiry, and CA bundle. Unannotated Pods should remain unaffected because the webhook match condition excludes them at the API server.

## The application is waiting in init

This is the expected fail-closed state until the Pod-UID-bound allocation ConfigMap exists. Inspect the Pod events and its controller-owned `VPNWorkload`. Do not create the ConfigMap manually; deleting and recreating it cannot safely substitute the recorded Pod UID and allocation generation.

## The gateway is not ready

Inspect conditions in order: `Scheduled`, `TunnelReady`, `OverlayReady`, `DNSReady`, then `Ready`. Common causes are a missing credentials Secret key, an unsupported or mutable engine image, unavailable `/dev/net/tun`, blocked provider connectivity, blocked UDP 4789, or an invalid cluster resolver observation.

The manager logs exclude provider response bodies, but logs can still contain operational IP addresses. Treat debug output as sensitive infrastructure metadata. Never print or decode the credentials Secret while collecting diagnostics.

## DNS fails only for protected Pods

Confirm the gateway remains ready and UDP/TCP 53 from the application is transparently redirected to the overlay. Kubernetes names use the pre-engine observed cluster resolver; external names use Gluetun's protected loopback resolver. If the gateway is absent, DNS failure is intentional and must not be bypassed by replacing the Pod's nameserver.

## A drain is blocked

Each singleton gateway has `minAvailable: 1`, so voluntary eviction is blocked while it is the only healthy replica. Move members to another explicitly configured gateway or schedule an intentional outage, then delete the gateway Pod yourself. Do not remove the PDB as a routine drain workaround.

## Removing protection

Remove the gateway annotation from the workload's Pod template and roll the workload. Existing Pods remain protected until deleted; Waycloak never rewires a running application from protected to ordinary egress.
