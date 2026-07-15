# Advanced configuration

This guide collects the decisions an operator makes after the basic
[getting-started](../getting-started.md) path is healthy. It links to the
normative API and architecture documents where exact behavior matters.

## Gateway and namespace topology

`VPNGateway` is namespaced. A protected Pod selects exactly one gateway:

```yaml
networking.waycloak.io/gateway: <namespace>/<name>
```

A bare name is accepted only when the gateway and workload share a namespace.
For cross-namespace use, the gateway authorizes workload namespaces with
`spec.workloadAccess.namespaceSelector`.

Prefer a dedicated namespace for gateways and a deliberate label shared only
by namespaces allowed to use that gateway:

```yaml
spec:
  workloadAccess:
    namespaceSelector:
      matchLabels:
        networking.waycloak.io/egress-class: proton-eu
```

Admission rejects unauthorized references before a Pod is created. An empty
selector matches all namespaces and should be treated as an explicit
cluster-wide trust decision.

You may declare multiple independent gateways and select them by name, but
v0.2.0 does not automate sharding, failover, or gateway replacement. Each
gateway is a singleton with its own tunnel and credentials.

## Overlay identity

Each gateway needs:

- a non-overlapping IPv4 overlay CIDR;
- a VNI not reused by another reachable overlay;
- an MTU that accounts for both VXLAN and VPN encapsulation.

The first usable overlay address belongs to the gateway. Client addresses are
allocated durably and quarantined after deletion. Do not derive operational
meaning from their numeric order.

The overlay endpoint uses UDP 4789. Restrict that traffic to the relevant
gateway and protected-workload nodes. Waycloak also enforces observed overlay
membership in the gateway network namespace; this does not replace CNI or host
firewall policy.

## Cluster-local traffic modes

Choose `spec.clusterTraffic.mode` explicitly:

- `Preserve` keeps declared Pod, Service, and other cluster-local CIDRs on the
  normal CNI path while external traffic uses the VPN. This is the usual mode.
- `Gateway` sends non-control traffic, including cluster-local destinations,
  through the selected gateway.
- `Deny` blocks cluster-local traffic except destinations permitted by the
  implemented control path.

`Preserve` requires an explicit `cidrs` list. Waycloak does not infer trusted
cluster networks from all RFC1918 ranges and does not request broad Node RBAC
to guess them. Revisit the list when the cluster's Pod or Service ranges
change.

See [Networking](../architecture/networking.md#cluster-local-modes) for routing
priority and DNS behavior.

## DNS

`dns.mode: Gateway` transparently redirects application UDP and TCP port 53 to
the gateway. Cluster zones are forwarded to the observed Kubernetes resolver;
external names use Gluetun's protected resolver path.

Applications do not need Waycloak-specific resolver configuration. External
encrypted DNS is still ordinary external traffic and therefore follows the
protected default route, but Waycloak does not inspect or classify its
application payload.

## Engine configuration and secret systems

The generic contract is that Waycloak consumes an operator-configured VPN
engine and reserves only the settings required for observed health, tunnel
identity, firewall handoff, and port-forward ownership. Provider, protocol,
server-selection, and custom configuration otherwise belong to the engine's
native configuration surface. See
[ADR 0017](../decisions/0017-engine-native-configuration-boundary.md).

The current `v0.2` compatibility API still uses
`spec.provider.credentialsSecretRef` and translates a limited provider shape
into Gluetun settings. That reference names an ordinary Kubernetes Secret in
the gateway namespace. The verified Proton/OpenVPN example expects `username`
and `password` keys. Engine-native configuration and migration of these fields
is tracked by issue #66.

ESO, Secrets Store CSI, SOPS-driven GitOps, or another system may materialize
that Secret. Waycloak neither depends on nor talks to those systems. Keep the
provider Secret out of protected workload namespaces and never duplicate its
values into application manifests.

Proton NAT-PMP additionally requires the provider's port-forward-compatible
OpenVPN username convention. Waycloak does not read or rewrite the username.

## Port-forward modes

Port forwarding is disabled unless the gateway explicitly enables a driver:

```yaml
spec:
  portForwarding:
    enabled: true
    driver: ProtonNatPmp
```

Each inbound consumer creates a `PortForwardLease`. The initial implementation
requires its selector to resolve to exactly one protected Pod using the same
gateway.

### Fixed application port

The default `applicationPortMode: Fixed` keeps the application listener on
`spec.target.port`. Gateway DNAT translates the provider's current public port
to that stable target. No application adapter is required unless the
application advertises its external port in an application protocol.

### Provider-assigned application port

`applicationPortMode: ProviderAssigned` asks an explicit adapter to apply the
current provider port to the application. The Pod agent installs the required
local redirect and reports delivery only after the adapter acknowledges the
exact lease generation and application port.

The neutral delivery document is available:

- as `/run/waycloak/port-forward/port-forward-leases.json` when the Pod selects
  one application container with
  `networking.waycloak.io/port-forward-container`;
- through the agent's Pod-loopback endpoint at
  `http://127.0.0.1:9809/v1/port-forward/leases`.

Do not give an adapter Kubernetes or VPN credentials. The
[qBitTorrent example](../../examples/qbittorrent/README.md) demonstrates the
supported narrow exception.

## Helm installation choices

Helm is the primary installer. Releases contain immutable controller, agent,
and gateway-manager identities in the packaged defaults.

Webhook TLS has two supported modes:

- externally managed Secret plus an explicitly supplied CA bundle;
- optional cert-manager resources and CA injection when cert-manager already
  exists.

The control plane supports node selectors, tolerations, affinity, replica
count, resource requests/limits, ServiceAccount annotations, and a disruption
budget through chart values. Inspect the exact release values before applying
site overrides:

```sh
helm show values waycloak-0.2.0.tgz
helm show readme waycloak-0.2.0.tgz
```

Protected agents and gateway Pods currently do not have configurable resource
requests in the v0.2.0 API. Account for that when applying quota or eviction
policy.

## Optional KCL authoring

KCL is an authoring adapter over the same Kubernetes API, not an installation
or runtime dependency. Verify the KCL OCI reference from the signed release
manifest, add the matching semantic tag, and commit the lock file:

```sh
kcl mod add oci://ghcr.io/amoenus/waycloak-kcl --tag 0.2.0
```

The module exposes `VPNGateway` and `PortForwardLease` schemas plus canonical
annotation helpers. `VPNWorkload` is generated for inspection but remains
controller-owned.

## Upgrades and gateway maintenance

Upgrade in phases:

1. verify the new signed release and upgrade the controller/webhook;
2. wait for every controller replica to run the intended immutable version;
3. deliberately replace singleton gateway Pods whose templates changed;
4. roll protected workloads so admission injects the intended agent version;
5. verify gateway, workload agent, and lease observations.

Gateway StatefulSets use `OnDelete`; the controller does not restart a working
tunnel just because desired configuration changed. v0.2.0 also requires a
protected workload rollout after gateway Pod replacement. Follow
[Upgrade and rollback](upgrade.md) rather than treating the gateway like an
ordinary rolling Deployment.

## Observability

The controller exposes Prometheus metrics and Kubernetes health endpoints.
Gateway manager and workload agents expose readiness endpoints and report
failures through logs, CRD conditions, and events.

Use Kubernetes conditions as the canonical dependency chain, but read the
[v0.2.0 status boundaries](troubleshooting.md#understand-v020-status-boundaries)
before alerting on every transition. Provider public IPs and operational
underlay addresses are sensitive infrastructure metadata even when they are
not credentials.

## Deeper references

- [API contract](../api/api-contract.md)
- [Detailed architecture](../architecture/architecture.md)
- [Networking design](../architecture/networking.md)
- [Threat model](../security/threat-model.md)
- [Developer experience](../product/developer-experience.md)
- [Architecture decisions](../decisions/README.md)
