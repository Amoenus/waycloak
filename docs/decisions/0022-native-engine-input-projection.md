# ADR 0022: Native engine inputs use ConfigMap environment and engine-only file mounts

Status: Accepted
Date: 2026-07-15

## Context

ADR 0017 assigns provider and protocol configuration to the VPN engine's
native model. The Kubernetes API still needs a precise reference shape that
can represent Gluetun providers, WireGuard, and custom OpenVPN files without
letting native settings override Waycloak's fail-closed integration boundary.
The v0.2 `provider` object must remain usable while installations migrate.

Kubernetes `envFrom` can project a ConfigMap into one container without
copying values into a workload spec. Secret volumes can project credential
files directly into the engine without granting the controller or gateway
manager Secret read access. Gluetun supports native custom OpenVPN paths and a
WireGuard configuration file under `/gluetun/wireguard`.

## Decision

`VPNGateway.spec.engine.config` has two inputs:

- ordered same-namespace ConfigMap references under `envFrom`;
- ConfigMap or Secret file sources with explicit engine mount paths under
  `files`.

The legacy `provider` object and `engine.config` are mutually exclusive. The
controller reads only referenced ConfigMaps. It validates environment-key
syntax, rejects reserved settings without reporting their values, and hashes
the ordered non-secret content into an opaque Pod-template annotation. A
ConfigMap change therefore emits the existing `GatewayRolloutRequired` event
without placing native values in a CR, generated ConfigMap, status, or event.

The controller never reads a referenced Secret. Kubernetes mounts that Secret
read-only only into the engine container. The gateway manager, init container,
and protected workloads receive neither the mount nor a Secret-capable API
token.

Waycloak reserves the tunnel interface, DNS bind, health and control-server
addresses, control authentication, public-IP observation, firewall settings,
and Gluetun port-forward settings. Native file mounts may not mask Waycloak
runtime paths, `/dev`, `/etc/resolv.conf`, `/iptables`, or the `/gluetun` state
root. Mounts are limited to nested `/gluetun/...` paths and the dedicated
`/run/engine-native` tree.

The optional Proton NAT-PMP driver checks the effective non-secret native
provider and protocol before resource reconciliation. Readiness still requires
the runtime provider lease and manager observations; desired configuration
alone never proves capability.

## Consequences

- New Gluetun providers and native options do not require Waycloak API fields.
- OpenVPN, WireGuard, and custom-provider file shapes are representable while
  credentials remain engine-only.
- Native ConfigMap changes require deliberate activation because gateway
  StatefulSets remain `OnDelete`.
- Secret rotations remain kubelet-projected, but an engine that does not
  reload the file requires an explicit gateway maintenance restart.
- Rollback below v0.3 requires converting native gateways back to the legacy
  `provider` shape first. An old controller must fail closed on an unsupported
  native object.
- Secret content cannot participate in controller-side compatibility checks or
  rollout digests. Runtime observation remains the authority for capabilities.

## Alternatives rejected

- Arbitrary Pod environment and volume fragments: exposes unrelated container
  controls and permits integration-path masking.
- Secret-backed `envFrom`: reserved-key inspection would require the controller
  to read credential values.
- Copy native settings into a controller-owned ConfigMap: duplicates
  operator-owned data and expands value disclosure.
- Remove `provider` immediately: prevents a staged pre-1.0 migration and safe
  rollback preparation.
