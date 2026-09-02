# Advanced setup

This guide covers configurations beyond the same-namespace quick path. Start
with a healthy exact `v1.0.1` installation and read the
[configuration reference](configuration.md) before changing the data plane.

## Cross-namespace routing

A route may reference a gateway in another namespace only when the gateway
explicitly consents. The safest shared-gateway pattern uses an
operator-controlled namespace label:

```sh
kubectl label namespace media waycloak.io/shared-gateway=approved
```

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNGateway
metadata:
  name: shared
  namespace: network-egress
spec:
  gatewayClassName: gluetun.waycloak.io
  clusterTraffic:
    mode: TunnelAll
  allowedRoutes:
    namespaces:
      from: Selector
      selector:
        matchLabels:
          waycloak.io/shared-gateway: approved
  # credentialRefs, nativeConfigRefs, and DNS omitted here
```

The workload namespace then owns a local route that points to the shared
gateway:

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: private
  namespace: media
spec:
  parentRefs:
    - group: networking.waycloak.io
      kind: VPNGateway
      namespace: network-egress
      name: shared
```

Keep consent labels outside tenant write authority. `from: All` is explicit but
broad and should be rare. Waycloak does not use Gateway API `ReferenceGrant`
for this contract. Read the [cross-namespace security model](security/cross-namespace-consent.md).

## Dedicated gateway nodes

Use standard Kubernetes placement fields on the gateway:

```yaml
spec:
  placement:
    nodeSelector:
      node-role.waycloak.io/gateway: "true"
    tolerations:
      - key: node-role.waycloak.io/gateway
        operator: Equal
        value: "true"
        effect: NoSchedule
```

This selects the gateway Pod; it does not change which nodes can run enrolled
workloads. The install plan's architecture selection controls the privileged
CNI and node-agent footprint. A node must have current authenticated baseline
capability before admission schedules an enrolled Pod there.

## Cluster traffic policy

`BypassCluster` keeps explicitly identified cluster/private destinations off
the VPN tunnel while internet traffic remains protected. `TunnelAll` requests
the stricter mode supported by the selected class. When adding
`bypassCIDRs`, include only reviewed infrastructure destinations and ensure they
cannot become a path to ordinary internet egress.

Treat a cluster DNS Service, API address, Pod/Service CIDR, node/LAN network,
and the Waycloak overlay as distinct networks. Run preflight again after
material cluster-network changes.

## Port forwarding

Port forwarding must be enabled in a reviewed install plan:

```sh
waycloakctl install plan \
  --context my-cluster \
  --release-manifest release-manifest.json \
  --overlay-cidr 100.96.0.0/16 \
  --namespace waycloak-system \
  --node-architecture amd64 \
  --enable-port-forwarding \
  --port-forward-controller-tls-secret waycloak-port-forward-controller-tls \
  --output json >install-plan.json
```

The immutable mTLS Secret must already contain the qualified controller client
identity, with `ca.crt`, `tls.crt`, and `tls.key` data. Its client certificate
must chain to the CA, permit client authentication, and contain only
`spiffe://waycloak.io/replacement-controller` as its URI identity. Keep the
Secret in `waycloak-system`, set `immutable: true`, and issue it through your
certificate source of truth; cert-manager is optional, not a runtime dependency.
The plan rejects partial configuration.

The gateway independently requests the feature and references one same-namespace
server-identity Secret:

```yaml
spec:
  requestedFeatures:
    - networking.waycloak.io/PortForwardServiceSingleActive
  credentialRefs:
    - name: proton-credentials
      role: networking.waycloak.io/OpenVPNCredentials
    - name: private-runtime-tls
      role: networking.waycloak.io/GatewayRuntimeTLS
```

The `GatewayRuntimeTLS` Secret is separate from the controller client Secret;
it is mounted only into the gateway runtime. After the gateway and route report
the optional feature ready, create a `PortForwardLease` whose backend is a
stable Service and whose policy is `SingleActive`.

Use a fixed internal application port whenever possible. Waycloak translates
the provider port to that Service port atomically. Enable the adapter protocol
and reference a `WorkloadAdapter` only when compatibility evidence shows the
application must learn or advertise the external port. Adapter credentials
remain workload-owned.

## Observability

Kubernetes conditions, events, and `waycloakctl doctor` are authoritative.
Metrics and traces explain failures but never drive readiness.

- Aggregate controller metrics are enabled by default.
- `observability.assets.enabled=true` adds ConfigMaps containing optional
  Prometheus rules and a Grafana dashboard.
- `observability.openTelemetry.otlpEndpoint` enables bounded OTLP/HTTP export
  to an existing collector or receiver.
- No Prometheus Operator, Grafana, collector, or tracing backend is a runtime
  dependency.

Signals deliberately exclude credentials, endpoints, provider ports, object
UIDs, arbitrary domains, and workload names. See
[Observability](operations/observability.md) for scrape configuration and alert
semantics.

## GitOps

Keep these concerns separate:

- the verified chart digest and plan-derived values describe the installed
  runtime;
- KCL or YAML describes user-authored gateways, routes, leases, adapters, and
  workload templates;
- Secrets come from a dedicated credential source; and
- controller-authored bindings and status stay out of Git.

For an initial install, commit reviewed render evidence but apply the exact
plan with `waycloakctl`. For an upgrade or rollback, suspend automatic runtime
sync, execute the reviewed transition, verify health, then resume GitOps. See
the [Helm and OCI guide](guides/helm.md#gitops-handoff).

## Upgrade, rollback, and repair

Verify the target release independently and generate a new plan against the
live source state. The plan binds the source Helm revision, release identity,
images, CRDs, gateway-class UID, CNI receipt, and observation certificates.
Apply repeats those checks before mutation and preserves denial throughout the
transition.

Rollback follows the same process with a verified older target. Do not use raw
`helm upgrade`, `helm rollback`, or manual deletion of the immutable default
class. Use `waycloakctl install repair plan/apply` only for the enumerated
pending/corrupt Helm recovery path documented in
[Turnkey bootstrap](implementation/turnkey-bootstrap.md).

## Backup and restore

`waycloakctl state backup` creates portable intent, not a cluster snapshot. It
includes the specs and names of user-authored gateways, routes, leases, and
adapters. It excludes Secrets, ConfigMap contents, workloads, bindings,
allocations, UIDs, status, and runtime observations.

Restore the exact signed release, namespaces, credential Secrets, and native
ConfigMaps from their own sources first, but keep enrolled workloads stopped.
Then use `waycloakctl state restore plan/apply`. Restored resources receive new
UIDs and must reacquire all runtime identity. Restore workload controllers in
controlled batches only after the intent is ready; enrolled workloads remain
denied until fresh bindings and readiness are observed.

A distribution-native K3s datastore restore is a separate, whole-cluster
procedure with confidential snapshot and server-token handling. Follow
[Backup and disaster recovery](operations/backup-restore-and-disaster-recovery.md)
exactly.

## Diagnostic order

When a protected workload is unavailable, keep enrollment in place and inspect
from the cluster boundary inward:

1. `waycloakctl doctor --context <context> --output human`
2. selected node capability and the node-agent DaemonSet
3. `VPNGatewayClass` release identity and advertised features
4. `VPNGateway` conditions, gateway Pod, tunnel, and gateway DNS probes
5. `VPNEgressRoute` acceptance, references, consent, and required features
6. controller-created `VPNWorkloadBinding` identity and applied observation
7. for inbound traffic, `PortForwardLease`, Service endpoints, and adapter
   acknowledgement

Useful commands:

```sh
waycloakctl doctor --context my-cluster --namespace media --route private
kubectl -n media get vpngateway,vpnegressroute,vpnworkloadbinding -o wide
kubectl -n media describe vpngateway private
kubectl -n media get events --sort-by=.lastTimestamp
```

Do not troubleshoot by removing enrollment, adding application capabilities,
copying credentials into the workload, changing host routes, or bypassing the
CNI. Unavailability is the safe failure mode.
