# Waycloak

**Declarative, fail-closed private egress for Kubernetes.**

Waycloak routes explicitly opted-in Pods through a shared VPN gateway. A
workload selects a gateway with one annotation; Waycloak injects and operates
the networking needed to keep ordinary internet egress blocked whenever the
protected path is unavailable.

```yaml
spec:
  template:
    metadata:
      annotations:
        networking.waycloak.io/gateway: waycloak-egress/proton-eu
```

Waycloak is for operators who want selected Kubernetes workloads to use a VPN
without placing a VPN client in every application Pod. It is not a VPN
provider, a service mesh, a replacement CNI, or a claim of anonymity.

## Start here

If this is your first time using Waycloak, follow these pages in order:

1. [Getting started](docs/getting-started.md) — install v0.2.0, create a
   gateway, protect a disposable workload, and verify it.
2. [Architecture and ownership](docs/concepts/architecture-and-ownership.md) —
   understand the controller, webhook, gateway, injected agent, and optional
   application adapters.
3. [Security exceptions](docs/operations/security-exceptions.md) — understand
   why protected Pods and gateway Pods require narrowly controlled
   `NET_ADMIN` exceptions.
4. [Troubleshooting](docs/operations/troubleshooting.md) — follow conditions
   and logs from the workload to the gateway.
5. [Advanced configuration](docs/operations/advanced-configuration.md) — tune
   authorization, cluster traffic, DNS, port delivery, KCL, and lifecycle.

The full [documentation map](docs/README.md) links the API contract,
architecture, threat model, operations guides, and design decisions.

## What Waycloak manages

Waycloak installs a Kubernetes controller and admission webhook. For every
`VPNGateway`, it creates a singleton gateway Pod containing a VPN engine and
the Waycloak gateway manager. For every annotated workload Pod, admission
injects fail-closed init containers and a small routing agent.

Applications receive no VPN credentials, Kubernetes API token, or additional
Linux capabilities from Waycloak. The injected agent receives `NET_ADMIN`
because all containers in a Pod share one network namespace and the agent owns
that namespace's protected routing state.

Inbound provider port forwarding is optional and uses a separate
`PortForwardLease`. Ordinary applications continue listening on a stable local
port while the gateway translates the provider port. Applications such as
qBitTorrent that advertise a port inside their own protocol require a narrow,
explicit adapter; application-specific behavior never enters the controller
or gateway manager.

## Current release

The current stable release is
[v0.2.0](https://github.com/Amoenus/waycloak/releases/tag/v0.2.0). It publishes:

- signed Linux `amd64` and `arm64` controller, agent, gateway-manager, and
  qBitTorrent-adapter OCI images;
- a signed OCI Helm chart containing all served CRDs;
- an optional signed OCI KCL authoring module;
- checksums, SBOMs, build provenance, and a signed release manifest containing
  every immutable artifact digest.

v0.2.0 is verified on Kubernetes 1.35 and 1.36 with Kindnet and Flannel. Other
Kubernetes versions and CNIs are not yet compatibility claims. The initial
real provider integration is Gluetun with Proton VPN over OpenVPN; the initial
port-forward driver is Proton NAT-PMP.

Important current boundaries:

- each gateway is a deliberate singleton;
- protected Pods fail closed during gateway loss, but v0.2.0 requires a
  workload rollout after a gateway Pod replacement;
- real-provider qBitTorrent tracker, peer-ingress, and sustained DHT
  certification remains v0.3.0 work;
- Pod Security Admission cannot grant `NET_ADMIN` to only an injected
  container, so protected namespaces require an explicit security policy.

See [Project status](PROJECT_STATUS.md) and the
[roadmap](docs/implementation/roadmap.md) for completed and remaining scope.

## Core guarantees

- Opt-in is explicit on the workload Pod template.
- Unannotated Pods are not mutated.
- Protected traffic fails closed rather than silently using ordinary egress.
- VPN credentials stay in the gateway namespace.
- Workload allocations and port leases have stable identities.
- Applications do not need to share a Pod with the VPN engine.
- Plain Kubernetes is the primary API; Helm is the primary installer; KCL is
  optional.

## License

Waycloak is licensed under the [MIT License](LICENSE). Any Apache-2.0 material
adapted from `angelnu/pod-gateway` retains its upstream copyright and notice
requirements.
