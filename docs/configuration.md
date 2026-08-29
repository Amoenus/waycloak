# Configuration requirements

This page defines the operator inputs for `v0.1.0-rc.27`. Exact defaults and
schema constraints remain authoritative in the Helm values schema and CRDs.

## Cluster inputs

| Input | Requirement |
| --- | --- |
| Kubernetes | 1.36+ with stable admission policy APIs and NodeRestriction |
| Nodes | Linux, kernel 5.10+, `amd64` or `arm64` |
| Runtime/CNI | containerd with a preflight-recognized kindnet or Flannel layout |
| DNS | one observable CoreDNS Service address and cluster domain |
| Overlay | unused private IPv4 `/16`–`/29`, reviewed for all network overlaps |
| Privilege | cluster-scoped API/RBAC plus privileged CNI installer and node agent |
| Release | signed `release-manifest.json`; every chart/image/module identity is digest pinned |

`waycloakctl preflight` is the executable authority for these requirements. It
hashes cluster identity and observations into the plan; drift before apply is a
hard failure.

## Installation-owned values

Do not hand-author these values for a supported installation:

- release version and manifest digest;
- controller, CNI, node-agent, gateway-agent, gateway-runtime, Gluetun,
  qBittorrent-adapter, and pause image digests;
- CNI config, binary, and receipt host paths;
- selected node architecture;
- observation CA, serving identity, and rotation identity;
- cluster DNS Service address/domain and overlay CIDR;
- immutable default `VPNGatewayClass` release identity.

They are produced by `waycloakctl install plan` from the verified release and
live preflight observations. The plan is the configuration review surface.

## Gateway configuration

The RC quick path supports `VPN_SERVICE_PROVIDER=protonvpn` and
`VPN_TYPE=openvpn`. `waycloakctl gateway init` creates a non-secret ConfigMap
and `VPNGateway` reference. The same namespace must contain:

- a Secret with `username` and `password` keys;
- a namespaced RoleBinding from `waycloak-gateway-secret-reader` to the
  Waycloak controller ServiceAccount;
- an unused gateway name and referenced ConfigMap name.

Credential values are mounted only into the VPN engine. They do not enter
protected workloads, release metadata, status, support bundles, or Helm values.

## Route and workload configuration

A `VPNEgressRoute` requires at least one parent reference. The baseline accepts
one effective gateway parent and can require the qualified features `TCP`,
`UDP`, and `DNSContainment`. Cross-namespace parents additionally require
gateway-side `allowedRoutes` consent.

Enrollment is exactly this Pod-template label:

```yaml
networking.waycloak.io/egress-route: <same-namespace-route-name>
```

It is not valid on a live-Pod mutation, and enrolled Pods cannot request host
namespaces. Existing scheduling constraints are preserved while Waycloak adds
the authenticated selected-node restriction.

## Optional port forwarding

Enable port forwarding only with `waycloakctl install plan
--enable-port-forwarding`. The plan requires a pre-created immutable controller
mTLS Secret and binds the gateway runtime identity. Add
`--enable-adapter-protocol` only when at least one explicitly selected,
operator-trusted `WorkloadAdapter` is intended. This enables the generic
adapter protocol; it does not install or select qBittorrent or any other
application integration.
See the `PortForwardLease` and `WorkloadAdapter` API reference before authoring
those resources.

Provider selection and provider-protocol ownership remain Gluetun-native. For
the qualified recipe, `VPN_SERVICE_PROVIDER=protonvpn` with
`VPN_TYPE=openvpn` selects
`gluetun.waycloak.io/native-port-forward`. Waycloak enables Gluetun's native
port-forward lifecycle and observes the resulting single shared TCP/UDP port
through an API-key-authenticated loopback control route. The key and narrow
control policy are derived at Pod initialization from the existing runtime TLS
identity and held only in a memory-backed volume. They are not Kubernetes API
credentials and are not copied into protected workloads.

Gluetun owns provider acquisition, renewal, and release. Waycloak owns stable
lease and target identities, provider-port translation, observation freshness,
handoff, application acknowledgement, status, atomic packet rules, and
fail-closed withdrawal. Because the Gluetun API does not expose provider TTL,
the frozen status `expiresAt` field is a bounded observation-validity deadline,
not a claimed provider lease expiry. Other Gluetun configurations requesting
port forwarding are rejected until they have explicit conformance evidence.

Provider-initiated packets cross the shared gateway firewall only after the
lease-owned prerouting chain matches the exact protocol and provider-internal
port and assigns Waycloak's reserved packet mark. Both the Gluetun filter
chain and the baseline gateway chain require that mark for a new
tunnel-to-overlay flow. The mark is local packet metadata: workloads cannot
set it, it is not sent over either network, and removing a lease atomically
removes the rule that can assign it.

## Configuration invariants

- Install exactly one Waycloak release per cluster.
- Never use mutable image or chart references.
- Never place VPN credentials in workload Pods or Git.
- Never manually mutate the distribution-owned default gateway class.
- Never treat Helm upgrade alone as CRD migration or safe runtime activation.
- Treat `Ready=True` as a current data-plane observation, not registration.
