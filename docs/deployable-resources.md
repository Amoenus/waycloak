# Deployable resources and ownership

Waycloak separates distribution, operator, workload, and controller ownership.
Users should author only the resources assigned to their role.

## Installed by the Helm chart

The exact chart installs:

- six `networking.waycloak.io/v1beta1` CRDs;
- the replacement controller Deployment and Service;
- the mandatory chained-CNI installer DaemonSet;
- the privileged node-agent DaemonSet;
- stable validating and mutating admission policies and bindings;
- release/runtime RBAC and unbound persona ClusterRoles;
- the optional immutable default `VPNGatewayClass`;
- optional bounded metrics, Prometheus rules, and Grafana dashboard assets.

The installer also creates release-owned observation trust Secrets directly in
Kubernetes. Secret values are not represented in Helm values.

## Operator-authored resources

| Kind | Scope | Owner | Purpose |
| --- | --- | --- | --- |
| `VPNGatewayClass` | Cluster | Distribution | Immutable implementation, release, and capability identity |
| `VPNGateway` | Namespace | Cluster/network operator | Provider references, placement, DNS, cluster-traffic, and route consent |
| Secret | Namespace | Gateway operator | Provider credentials; referenced, never copied |
| ConfigMap | Namespace | Gateway operator | Non-secret native engine inputs |
| RoleBinding | Namespace | Cluster/gateway operator | Least-privilege persona and credential-reader grants |
| `VPNEgressRoute` | Namespace | Workload owner | Selects an allowed gateway and required features |
| `PortForwardLease` | Namespace | Workload owner/operator | Optional typed Service-backed single-active provider mapping; fixed backend ports need no adapter |
| `WorkloadAdapter` | Namespace | Adapter operator | Last-resort application-specific handoff trust record; never a VPN/provider plugin |
| Workload Pod template | Namespace | Workload owner | Explicit route enrollment label |

The [workload-adapter guide](guides/workload-adapters.md) shows the additional
Service, Deployment, PVC, PKI, application credential, trust-record, and lease
ownership required by the qBittorrent reference integration. The
[dependency boundary](dependencies.md) identifies which configuration remains
owned by Gluetun, qBittorrent, Kubernetes, and other upstream projects.

## Controller-authored resources

`VPNWorkloadBinding` is controller owned. It binds one exact Pod UID to route,
gateway, node, allocation, network projection, and observed agent state. Users
must not create, patch, restore selectively, or reuse it as desired state.

The controller also owns gateway runtime Pods and status. The node agent owns
node packet state and durable CNI attachments. Application Pods contain no
Waycloak networking containers or credentials.

## CRD and uninstall behavior

Helm installs CRDs from `crds/` before namespaced resources. Helm neither
upgrades those CRDs as an ordinary template nor deletes them during uninstall.
Normal uninstall, destructive CRD purge, and primary-CNI restoration are
separate operations with separate confirmation boundaries. Follow the lifecycle
runbook rather than deleting cluster-scoped resources manually.
