# API contract

This document proposes the `v1alpha1` API. Field names may change before the first release, but implementations must preserve the product invariants.

API group: `networking.waycloak.io`
Initial version: `v1alpha1`

## VPNGateway

Namespaced declaration of one VPN tunnel and its shared overlay.

```yaml
apiVersion: networking.waycloak.io/v1alpha1
kind: VPNGateway
metadata:
  name: proton-eu
  namespace: private-egress
spec:
  engine:
    type: Gluetun
    image: ghcr.io/qdm12/gluetun@sha256:REQUIRED_DIGEST
  provider:
    name: protonvpn
    protocol: openvpn
    region: Netherlands
    credentialsSecretRef:
      name: protonvpn-credentials
  overlay:
    cidr: 172.30.99.0/24
    vni: 7999
    mtu: 1320
  dns:
    mode: Gateway
  clusterTraffic:
    mode: Preserve
  portForwarding:
    enabled: true
    driver: ProtonNatPmp
  workloadAccess:
    namespaceSelector: {}
```

The actual image API should separate chart-tested defaults from explicit user override while still recording an immutable resolved digest in status. For the initial Gluetun adapter, `provider.region` is a country selector and maps to Gluetun's `SERVER_COUNTRIES`; the field remains provider-neutral at the Kubernetes API boundary.

For the initial Proton/OpenVPN Gluetun integration, `credentialsSecretRef` names a Secret in the gateway namespace with `username` and `password` keys. Waycloak mounts the Secret only into the engine container and configures Gluetun's secret-file settings; it does not copy values into status, manager configuration, or protected workloads. Additional protocol-specific keys require a documented API addition.

### Gateway conditions

- `Accepted`: spec is valid and authorized.
- `Scheduled`: gateway Pod has placement.
- `TunnelReady`: VPN engine reports healthy.
- `OverlayReady`: gateway overlay is configured.
- `DNSReady`: configured resolver path is healthy.
- `PortForwardReady`: driver is usable or explicitly disabled.
- `Ready`: all requirements for serving clients are observed.

Status includes provider capabilities, current client count, address-pool usage, resolved image digests, observed public IP with configurable redaction, and last health verification. The controller also records the observed VXLAN underlay endpoint and overlay health port used in each UID-bound allocation handshake; workloads never infer those values from desired registration.

## Workload annotation

Canonical selection:

```yaml
networking.waycloak.io/gateway: private-egress/proton-eu
```

The gateway namespace may be omitted only when the gateway and workload share a namespace. Cross-namespace selection is authorized by the gateway's `spec.workloadAccess.namespaceSelector`; admission rejects a reference whose selector does not match the workload namespace.

Optional request:

```yaml
networking.waycloak.io/port-forward: tcp,udp
```

This annotation is reserved as future workload-template convenience. The first
Phase 4 control-plane slice uses an explicit `PortForwardLease`; admission does
not create lease intent from the annotation yet.

Injection markers are reserved under `internal.networking.waycloak.io/*` and are controller-owned.

## VPNWorkload

`VPNWorkload` is a controller-owned, publicly inspectable namespaced registration. Users do not author it. This gives stable allocation and status without requiring intent in two places.

Illustrative form:

```yaml
apiVersion: networking.waycloak.io/v1alpha1
kind: VPNWorkload
metadata:
  name: pod-uid-prefix
  namespace: media
  ownerReferences:
    - apiVersion: v1
      kind: Pod
      name: qbittorrent-abc
      uid: 00000000-0000-0000-0000-000000000000
spec:
  podRef:
    name: qbittorrent-abc
    uid: 00000000-0000-0000-0000-000000000000
  gatewayRef:
    namespace: private-egress
    name: proton-eu
status:
  allocation:
    address: 172.30.99.12
    generation: 1
  conditions: []
```

The allocation is created once and persisted. Address reuse after deletion requires a quarantine interval to prevent stale route or DNAT delivery.

The webhook also injects a non-optional ConfigMap volume with a deterministic name derived from namespace and Pod name. After Pod creation, the controller creates that ConfigMap from the persisted `VPNWorkload` allocation and binds it to the Pod UID. Until it exists, kubelet cannot start the init component or application. A same-name replacement never reuses a ConfigMap owned by a different Pod UID.

## PortForwardLease

```yaml
apiVersion: networking.waycloak.io/v1alpha1
kind: PortForwardLease
metadata:
  name: qbittorrent
  namespace: media
spec:
  gatewayRef:
    namespace: private-egress
    name: proton-eu
  target:
    podSelector:
      matchLabels:
        app.kubernetes.io/name: qbittorrent
    port: 6881
  protocols:
    - TCP
    - UDP
status:
  providerInternalPort: 49152
  publicPort: 52197
  issuedAt: "2026-07-13T11:30:00Z"
  renewAfter: "2026-07-13T12:30:00Z"
  expiresAt: "2026-07-13T13:00:00Z"
  leaseGeneration: 4
  conditions: []
```

The selector must be non-empty. The initial implementation requires exactly
one Ready target Pod and marks the lease ambiguous otherwise. The target is
accepted only when that Pod selects the same gateway and its controller-owned
`VPNWorkload` binds the exact Pod UID to a persisted overlay allocation.
Status records that observed Pod UID, workload reference, overlay address, and
local port. The `PortForwardLease` object UID is the stable provider-facing
lease identity. The controller also persists a unique NAT-PMP internal port;
neither value is derived from list order, and deletion quarantines the mapping
identity across provider expiry. A future Service target may support controlled handoff during
rolling updates after its identity and drain semantics are proven
([ADR 0012](../decisions/0012-port-forward-lease-identity-and-target-binding.md)).

### Lease conditions

- `Accepted`
- `ProviderLeaseReady`
- `GatewayRulesReady`
- `TargetReady`
- `Delivered`
- `Ready`

`Ready=True` means the provider lease is current, gateway rules are installed for the observed generation, the target identity is current, and the exact renewable delivery record has been acknowledged by the target Pod agent. ConfigMap publication or object registration alone is insufficient.

`GatewayRulesReady=True` specifically means the serving gateway read back both
the prerouting DNAT and forward-accept rules for the lease object UID, current
`leaseGeneration`, protocol set, and exact UID-bound overlay target. A current
provider mapping without those exact rules leaves the condition false. Target
changes update gateway rules without rotating the provider mapping. `Delivered`
remains a separate observation of the renewable application-facing record.

Provider behavior is behind an observed capability interface describing
supported protocols, simultaneous lease capacity, shared TCP/UDP port
semantics, requested-port support, and minimum duration. Repeated ensure calls
carry the stable lease identity and are idempotent. Provider acquisition never
owns gateway DNAT or application delivery state.

The initial `ProtonNatPmp` driver is supported with `provider.name:
protonvpn` and `provider.protocol: openvpn`. Proton requires the referenced
OpenVPN username to include `+pmp`; Waycloak does not read or rewrite that
Secret value. Gluetun selects port-forward-capable servers but its own
port-forward loop is disabled so the gateway manager remains the only mapping
owner. Provider acquisition is observed through the exact serving gateway Pod
and increments `leaseGeneration` only when the public port changes
([ADR 0013](../decisions/0013-proton-nat-pmp-ownership-and-observation.md)).

`spec.target.port` is the stable gateway-to-Pod target and, in the default
`Fixed` mode, the application listener. The public provider port in status may
rotate; gateway DNAT absorbs that transport change. Applications
that advertise an external endpoint still need the current public port through
the generic mapping presentation or neutral delivery contract; packet-header
translation alone cannot rewrite application protocol messages
([ADR 0015](../decisions/0015-stable-target-port-translation.md)).

`spec.target.applicationPortMode` defaults to `Fixed`. `ProviderAssigned`
retains `spec.target.port` as the stable gateway-to-Pod target but requires the
application to bind the delivery record's current `applicationPort`. The Pod
agent publishes delivery only after an adapter acknowledges the exact lease
generation and its native local redirect is reconciled. Generation change,
expiry, or agent restart clears that observation fail closed
([ADR 0016](../decisions/0016-provider-assigned-application-port-handoff.md)).

The canonical renewable delivery record is versioned JSON exposed through an
atomically replaced read-only file and a read-only Pod-loopback endpoint. A Pod
may select exactly one application container with
`networking.waycloak.io/port-forward-container: <container>`. Admission then
projects only `port-forward-leases.json` from the UID-bound allocation
ConfigMap into `/run/waycloak/port-forward`; the application does not receive
the other allocation keys, Kubernetes credentials, or added capabilities. The
same document is served on loopback port `9809` under
`/v1/port-forward/leases` and `/v1/port-forward/leases/<identity>`.

The controller reports `Delivered=True` only after the target agent readback
matches the API version, lease object UID, target Pod UID, lease generation,
and canonical unexpired expiry. A controller-owned content-digest Pod
annotation prompts kubelet to refresh the short-lived projected record without
restarting the Pod. Agent acknowledgement proves neutral record delivery, not
application-specific configuration; adapters that support stronger
acknowledgement add that observation at their own boundary.

Kubernetes environment variables are not a renewable delivery surface. An
environment-only application explicitly runs under a supervisor that stops its
child when the current generation expires or changes and starts it again only
after a ready record is available. The controller never rolls an arbitrary
workload owner to refresh environment state. [ADR 0011](../decisions/0011-renewable-port-lease-delivery.md)
fixes these semantics.

## Common condition conventions

All APIs use Kubernetes-style conditions with:

- `type`
- `status`
- `observedGeneration`
- `lastTransitionTime`
- stable machine-readable `reason`
- concise human-readable `message`

Controllers update status only from observations. Desired configuration does not imply readiness.

## Versioning

- Conversion is not required until a second served version exists.
- Unknown fields are rejected unless a field explicitly preserves them.
- Defaults are encoded in the CRD schema or webhook and documented.
- Renaming an annotation or changing fail-closed semantics is a breaking change.
- Status and reason values should remain backward compatible within a major version.
