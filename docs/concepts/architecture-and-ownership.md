# Architecture and ownership

This page explains which moving parts belong to Waycloak and which resources a
cluster or workload owner must provide.

## Runtime layout

```text
Kubernetes API
  |
  +-- Waycloak controller and admission webhook
  |     +-- reconcile VPNGateway and PortForwardLease
  |     +-- own VPNWorkload and allocation ConfigMaps
  |     `-- inject protected Pods
  |
  +-- gateway Pod (one per VPNGateway)
  |     +-- VPN engine: Gluetun
  |     `-- Waycloak gateway manager
  |
  `-- protected application Pod
        +-- Waycloak prepare and verify init containers
        +-- application container, unchanged
        +-- Waycloak agent sidecar
        `-- optional application adapter, explicitly declared
```

The control plane carries desired state and observations. Application traffic
does not pass through the controller or admission webhook.

## Cluster control plane

The Helm release installs:

- two controller/webhook replicas with leader election;
- `VPNGateway`, `VPNWorkload`, and `PortForwardLease` CRDs;
- least-privilege RBAC;
- webhook Services and admission configurations;
- controller health and Prometheus endpoints;
- a controller disruption budget.

The webhook is selected only for Pods carrying the canonical gateway
annotation. Unannotated Pods are not sent through Waycloak mutation.

For an annotated Pod, admission resolves the gateway, checks the gateway's
namespace selector, injects versioned components, and adds a required
allocation volume. Repeating the same admission mutation produces the same
result.

## Gateway Pod

Every `VPNGateway` creates one controller-owned StatefulSet. It is a deliberate
singleton in v0.2.0.

### VPN engine

Gluetun is the current external VPN engine. It owns the OpenVPN process and
tunnel interface. The provider credential Secret is mounted only into this
container.

Gluetun is not Waycloak code, but its exact image digest and supported
configuration are part of the tested Waycloak release contract.

### Gateway manager

The gateway manager is Waycloak code running in the same Pod network namespace
as Gluetun. It owns:

- gateway-side VXLAN and overlay membership;
- deny-first forwarding and masquerade rules;
- protected DNS proxying;
- tunnel, DNS, and overlay health observations;
- provider port-forward driver calls;
- lease-specific DNAT and forwarding-rule readback.

The Proton NAT-PMP driver discovers its peer from the observed OpenVPN tunnel
prefix. Provider behavior is isolated behind an interface rather than embedded
in Kubernetes reconcilers.

## Protected application Pod

All containers in a Kubernetes Pod share one network namespace. Waycloak uses
that property to protect an existing application without putting the VPN
engine in the application Pod.

### Allocation handshake

The controller creates a `VPNWorkload` and persists a stable overlay address.
It then publishes a deterministic allocation ConfigMap bound to the exact Pod
UID. Kubelet cannot start the injected init containers until that ConfigMap
exists. A same-name replacement Pod cannot reuse another Pod's allocation
record.

### Injected init containers

`waycloak-prepare` establishes deny-first policy and the initial owned network
state. `waycloak-verify` proves the observed gateway overlay path before the
application starts. If setup fails, ordinary internet egress remains blocked.

### Injected agent

`waycloak-agent` continuously repairs:

- the Pod-owned nftables policy;
- policy routes and VXLAN state;
- protected DNS redirection;
- gateway reachability;
- local port redirects and renewable lease delivery.

Only this infrastructure container receives `NET_ADMIN`. Waycloak does not add
capabilities, provider credentials, or Kubernetes credentials to application
containers.

## Port forwarding and adapters

A `PortForwardLease` binds a stable lease identity to one exact protected Pod
UID and local target port. The provider may rotate its public port without
renumbering the local target:

```text
peer -> provider public port -> VPN tunnel -> gateway DNAT
     -> workload overlay address -> stable target port
```

The controller publishes a renewable, versioned record through the Pod agent's
loopback HTTP API and a projected file. That record is provider- and
application-neutral.

Most applications need no adapter. An adapter is justified only when an
application advertises the external port inside its protocol and cannot learn
it through a standard mechanism. The qBitTorrent adapter is one such narrow
exception. It is Waycloak-built but explicitly declared by the workload owner;
admission does not inject it into unrelated applications.

## Who defines what

| Resource or component | Who defines it | Who operates it |
| --- | --- | --- |
| Helm release and webhook TLS | Cluster operator | Waycloak control plane |
| `VPNGateway` | Platform/network operator | Waycloak controller and gateway manager |
| Provider credentials Secret | Platform operator or external secret system | Mounted only into the VPN engine |
| Gateway StatefulSet, Service, ConfigMap, PDB | Waycloak controller | Waycloak and Gluetun |
| Workload namespace authorization label | Platform operator | Kubernetes admission |
| Gateway annotation on a Pod template | Workload owner | Waycloak admission |
| `VPNWorkload` | Waycloak controller | Waycloak controller; users inspect only |
| Allocation ConfigMap | Waycloak controller | Workload init containers and agent |
| Prepare/verify init containers | Waycloak admission | Waycloak |
| Workload agent sidecar | Waycloak admission | Waycloak |
| Application container | Workload owner | Workload owner |
| `PortForwardLease` | Workload owner | Waycloak controller, manager, and agent |
| qBitTorrent adapter and API-key Secret | Workload owner when needed | Waycloak-built adapter and qBitTorrent |
| KCL definitions | Optional authoring tool | No runtime dependency |

## Minimal declarations

For ordinary private egress, a platform operator creates one reusable gateway.
The workload owner adds only:

```yaml
networking.waycloak.io/gateway: <gateway-namespace>/<gateway-name>
```

For inbound reachability, the workload owner additionally creates a
`PortForwardLease`. No provider fields belong in the workload declaration.

## Trust boundaries

- A cluster administrator or node-root actor can bypass Pod networking and is
  outside the protection boundary.
- The provider credential Secret is trusted only by the gateway VPN engine.
- The controller can create infrastructure resources but does not receive VPN
  credentials.
- The agent is privileged only within its Pod network namespace.
- Application containers remain responsible for their own filesystem,
  process, and application-layer security.
- Waycloak guarantees selected, fail-closed VPN egress within its documented
  threat model. It does not guarantee anonymity.

See the [networking design](../architecture/networking.md) for packet paths and
the [threat model](../security/threat-model.md) for the complete boundary.
