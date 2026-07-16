# Real-provider port-forward acceptance

This gated suite proves the Phase 4 behavior that a local NAT-PMP fixture
cannot: actual Proton ingress, renewal, provider port rotation, qBitTorrent
advertisement, DHT health, and fail-closed recovery. It is destructive only to
resources it creates under the `waycloak-real-pf-` prefix. The test creates a
Gluetun-native `engine.config` gateway: non-secret provider, protocol, and
region settings live in a temporary ConfigMap, while the rotated credential
Secret is mounted read-only only into the engine container. The test process
enumerates only Secret key names and never reads VPN Secret values. Public
endpoint values are held in memory only for assertions and are never printed,
persisted, or published.

Run it only from a reviewed commit already contained in `main`, using a signed
pre-release produced by the protected tag workflow. A locally built controller,
agent, gateway manager, or qBitTorrent adapter is not acceptable evidence for
this release gate.

## Prerequisites

- a Kubernetes 1.35 or 1.36 Linux cluster where the operator has explicitly
  allowed this disruptive test;
- a verified, release-manifest-pinned Waycloak installation from the commit
  under test;
- a dedicated namespace carrying the documented Waycloak Pod Security
  exception;
- a newly rotated Proton/OpenVPN credential Secret in that namespace with
  opaque `username` and `password` keys;
- the signed qBitTorrent adapter digest from the same release manifest;
- an amd64 worker node with working VXLAN, nftables, netlink, and external
  ingress to the VPN endpoint.

Do not pass credential values through environment variables, command-line
arguments, test logs, or repository files. Provision the Secret from secure
files or the cluster's approved secret manager.

## Verify and install the pre-release

Follow [the installation guide](../operations/install.md), substituting the
pre-release tag for `version`. Verify the release manifest's exact GitHub
workflow identity and every digest before installing the chart. Select
`.artifacts.qbittorrentAdapterImage.reference` from that same verified manifest
for the test environment below.

The tag must point at a commit already contained in `origin/main`; the release
workflow enforces this before publishing. Do not move or reuse a tag whose
artifacts have already been consumed.

## Execute

Export only object names, an immutable adapter reference, and timing controls:

```sh
export WAYCLOAK_E2E_REAL_PORT_FORWARD=1
export WAYCLOAK_E2E_ALLOW_NON_KIND=1
export WAYCLOAK_REAL_VPN_NAMESPACE=waycloak-acceptance
export WAYCLOAK_REAL_VPN_SECRET=waycloak-proton-acceptance
export WAYCLOAK_REAL_QBITTORRENT_ADAPTER_IMAGE="$(
  jq -r '.artifacts.qbittorrentAdapterImage.reference' release-manifest.json
)"
export WAYCLOAK_REAL_PORT_FORWARD_SOAK=10m
export WAYCLOAK_REAL_PORT_ROTATION_TIMEOUT=1h

kubectl get secret -n "$WAYCLOAK_REAL_VPN_NAMESPACE" \
  "$WAYCLOAK_REAL_VPN_SECRET" \
  -o 'go-template={{range $key, $_ := .data}}{{$key}}{{"\n"}}{{end}}'
make e2e-real-port-forward
```

The preflight and test require exactly the `username` and `password` key names;
the template above does not render their values.

The test deliberately fails if the provider does not rotate the public port
within the configured timeout. Raising the timeout is valid; replacing actual
rotation with a fixture or manually editing status is not.

## Evidence asserted

The suite creates an ordinary probe Pod and an annotated qBitTorrent Pod. It
then proves:

- admission injected the protected Pod while leaving the ordinary Pod outside
  Waycloak;
- the gateway uses the v0.3 Gluetun-native ConfigMap and engine-only Secret file
  contract, with no legacy `provider` object;
- the protected public egress differs from ordinary egress without logging
  either value;
- the real lease reaches observed `Ready=True` for the exact target Pod UID;
- external TCP connection and a valid UDP DHT KRPC exchange reach qBitTorrent
  through the assigned provider endpoint;
- qBitTorrent's tracker advertisement exactly matches the current public port
  and its observed DHT node count remains non-zero;
- lease issue time advances and the public port rotates without replacing the
  qBitTorrent Pod;
- gateway loss regresses lease readiness, blocks protected egress, and makes
  both the stale TCP and stale UDP endpoint unreachable;
- recovery restores ingress, advertisement, and DHT while retaining the same
  application Pod UID.

The loopback tracker and qBitTorrent observer are test fixtures only. They do
not emulate Proton, NAT-PMP, the VPN tunnel, or public ingress.

## Credential incident boundary

If any command, log, artifact, or telemetry exposes a credential value, stop
the run immediately. Rotate the credential at its source of truth, invalidate
the old value, let the approved secret manager reconcile a dedicated
acceptance Secret, and begin a fresh run. Evidence collected with a known
exposed credential is not release evidence.
