# Troubleshooting

Start with Kubernetes conditions and events; they use stable reasons and do not contain credential values.

```sh
kubectl describe vpngateway -n waycloak-egress example
kubectl get vpnworkload -A
kubectl get events -A --sort-by=.lastTimestamp
```

Follow the runtime path in this order:

1. admitted workload Pod and its injected containers;
2. controller-owned `VPNWorkload` and allocation ConfigMap;
3. selected `VPNGateway` and gateway Pod;
4. optional `PortForwardLease`, agent delivery, and application adapter.

The
[architecture and ownership guide](../concepts/architecture-and-ownership.md)
identifies the relevant container at each step.

## Understand v0.2.0 status boundaries

In v0.2.0, the injected agent's readiness and `VPNGateway.Ready` are the
authoritative observations for the protected path. `VPNWorkload.Ready` is not
yet wired to the agent observation and can report
`Ready=False, reason=DataPlaneNotImplemented` even when the Pod agent is ready.
Use `Accepted`, `Allocated`, and `AllocationPublished` on `VPNWorkload`, then
check the Pod's `waycloak-agent` readiness and the gateway conditions.

A renewable `PortForwardLease` can briefly report `Delivered=False` with an
expiry-mismatch event while a same-port expiry refresh is projected and
acknowledged. It should recover within seconds without changing
`leaseGeneration` or restarting the Pod. Persistent failure, an expired
record, or a changed generation without adapter acknowledgement is not this
benign convergence case.

## An annotated Pod is rejected

- `GatewayNotFound`: check the `<namespace>/<name>` annotation.
- `GatewayUnauthorized`: label the workload namespace so it matches `spec.workloadAccess.namespaceSelector`.
- an injection-version error: roll the workload so a new Pod is admitted by the installed controller and immutable agent image.
- webhook TLS or availability error: check both controller replicas, the webhook Service endpoints, serving-certificate SAN, expiry, and CA bundle. Unannotated Pods should remain unaffected because the webhook match condition excludes them at the API server.

## The application is waiting in init

This is the expected fail-closed state until the Pod-UID-bound allocation ConfigMap exists. Inspect the Pod events and its controller-owned `VPNWorkload`. Do not create the ConfigMap manually; deleting and recreating it cannot safely substitute the recorded Pod UID and allocation generation.

Inspect the injected containers without dumping Secret-backed environment
values:

```sh
kubectl get pod -n WORKLOAD_NAMESPACE POD_NAME \
  -o jsonpath='{.spec.initContainers[*].name}{"\n"}{.spec.containers[*].name}{"\n"}'
kubectl describe vpnworkload -n WORKLOAD_NAMESPACE WORKLOAD_NAME
```

## The gateway is not ready

Inspect conditions in order: `Scheduled`, `TunnelReady`, `MembershipApplied`,
`OverlayReady`, `DNSReady`, then `Ready`. If `MembershipApplied=False`, compare
`status.overlay.desiredMembershipGeneration` and
`status.overlay.appliedMembershipGeneration`. A brief mismatch is ordinary
ConfigMap projection delay; a persistent `MembershipGenerationPending` or
`MembershipObservationFailed` event identifies a stuck projection, manager, or
tokenless observation path without requiring kernel inspection. Common causes
of other failures are a missing credentials Secret key, an unsupported or
mutable engine image, unavailable `/dev/net/tun`, blocked provider
connectivity, blocked UDP 4789, or an invalid cluster resolver observation.

The manager logs exclude provider response bodies, but logs can still contain operational IP addresses. Treat debug output as sensitive infrastructure metadata. Never print or decode the credentials Secret while collecting diagnostics.

Container ownership:

- `vpn-engine` logs describe Gluetun/OpenVPN and provider tunnel health;
- `waycloak-gateway-manager` logs describe overlay, DNS, forwarding, and
  provider port-forward reconciliation;
- the active `controller` replica reports Kubernetes reconciliation errors;
- `waycloak-agent` reports protected-path repair failures;
- an explicitly declared application adapter reports application handoff
  failures.

Gluetun records manager health polling as HTTP access logs. Repeated successful
`/v1/dns/status` and `/v1/publicip/ip` requests are noisy but are not tunnel
failures.

## A PortForwardLease is not ready

Inspect conditions in dependency order:

```sh
kubectl describe portforwardlease -n WORKLOAD_NAMESPACE LEASE_NAME
kubectl get events -n WORKLOAD_NAMESPACE \
  --field-selector involvedObject.kind=PortForwardLease,involvedObject.name=LEASE_NAME \
  --sort-by=.lastTimestamp
```

- `Accepted` validates the declaration and gateway compatibility.
- `TargetReady` binds exactly one ready, protected Pod UID.
- `ProviderLeaseReady` observes a current provider mapping.
- `GatewayRulesReady` reads back the exact DNAT and forwarding generation.
- `Delivered` observes the UID/generation-bound record in the workload agent
  and, for `ProviderAssigned`, the application adapter acknowledgement.
- `Ready` requires all of the above.

Do not infer peer ingress or DHT health from `Ready=True`; those are
application/provider acceptance checks.

## DNS fails only for protected Pods

Confirm the gateway remains ready and UDP/TCP 53 from the application is transparently redirected to the overlay. Kubernetes names use the pre-engine observed cluster resolver; external names use Gluetun's protected loopback resolver. If the gateway is absent, DNS failure is intentional and must not be bypassed by replacing the Pod's nameserver.

## A drain is blocked

Each singleton gateway has `minAvailable: 1`, so voluntary eviction is blocked while it is the only healthy replica. Move members to another explicitly configured gateway or schedule an intentional outage, then delete the gateway Pod yourself. Do not remove the PDB as a routine drain workaround.

## Removing protection

Remove the gateway annotation from the workload's Pod template and roll the workload. Existing Pods remain protected until deleted; Waycloak never rewires a running application from protected to ordinary egress.
