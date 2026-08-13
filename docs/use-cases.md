# Waycloak use cases

Waycloak is one Kubernetes-native product for selected, fail-closed VPN egress.
Its public API is plain Kubernetes; Helm is the primary installation surface and
KCL is an optional authoring package.

## Supported uses

### Protect one application

Create a `VPNGateway`, point a same-namespace `VPNEgressRoute` at it, and add the
route label to a Deployment, StatefulSet, Job, or other controller's Pod
template. The application Pod receives no Waycloak container, capability,
credential, Kubernetes token, or host mount.

### Share one gateway

Multiple explicitly enrolled workloads can share a gateway while retaining
UID-bound `VPNWorkloadBinding` identities and distinct overlay allocations.
Unlabelled workloads keep ordinary cluster networking.

### Separate gateway and workload namespaces

A route may reference a gateway in another namespace only when the gateway
owner explicitly allows the route namespace. Namespace-label selectors used for
consent must be controlled by the cluster operator, not tenants.

### Protect TCP, UDP, and DNS

The baseline contract covers TCP, UDP, fragmented UDP, DNS over UDP/TCP, and a
declared cluster-traffic policy. Readiness is withdrawn when the selected tunnel
or DNS path is not observed healthy.

### Deliver a provider-assigned port

The optional single-active port-forward capability binds a provider mapping to
one exact Service endpoint. By default the application keeps one stable local
port while Waycloak translates the changing provider port at the gateway; no
application adapter is needed. A separately selected adapter is used only when
the application itself must change or advertise the provider-assigned port.
The qBittorrent adapter is included as that reference exception. This
capability is enabled explicitly in the install plan and is not a separate
product.

### GitOps-managed clusters

Argo CD or another GitOps controller may own the exact chart declaration and
operator-authored resources. Changed-release activation still goes through the
journal-bound `waycloakctl install plan/apply` transaction before GitOps
converges the matching immutable release identity.

## Deliberate non-goals for v0.1

- anonymity claims or protection from a compromised node/cluster administrator;
- transparent enrollment of unlabelled workloads;
- sidecar, init-container, proxy-only, or application-capability modes;
- multiple active gateways, sharding, automatic gateway failover, or provider
  selection per connection;
- Windows nodes, non-containerd runtimes, or unlisted CNI layouts;
- host-networked, host-PID, or host-IPC application Pods;
- automatic alpha migration or import of old allocation and lease state;
- Bitmagnet certification.

Unsupported intent fails before protected workloads are created or remains
fail closed. It never silently falls back to ordinary internet egress.
