# qBitTorrent example

This example also installs the cluster-scoped `WorkloadAdapter` trust record
for the exact qBitTorrent adapter digest. The Pod template independently names
that trust record and its adapter container, so operator approval and workload
intent are both visible. Release rendering replaces both placeholder image
references with the same signed immutable digest.

This example keeps the declared Waycloak target at TCP/UDP `6881` while a
narrow, unprivileged adapter makes qBitTorrent listen on each current provider
port. Waycloak translates the stable target locally and translates outbound
listener traffic back to the provider mapping at the gateway. qBitTorrent's
ordinary configuration and consumers remain unaware of lease generations.

The manifests are intentionally disposable: configuration and downloads use
`emptyDir`. Replace both with persistent volumes before storing data.

## Prerequisites

- Install Waycloak and create `waycloak-system/private-egress` with port
  forwarding enabled.
- Authorize namespaces labeled
  `networking.waycloak.io/example=qbittorrent` in the gateway's
  `spec.workloadAccess.namespaceSelector`.
- If your gateway has another name, update both the Pod annotation and
  `PortForwardLease.spec.gatewayRef` together.
- Create a qBitTorrent API key and the workload-owned adapter Secret. The key
  must contain exactly 32 characters: qBitTorrent's `qbt_` prefix followed by
  28 random alphanumeric characters.
  Store the same key in `WebUI\APIKey` in `qBittorrent.conf`, bind the Web UI to
  loopback, and disable qBitTorrent UPnP/NAT-PMP. For example:

```sh
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
umask 077
random=""
while [ "${#random}" -lt 28 ]; do
  random="${random}$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9')"
done
random="$(printf '%s' "$random" | cut -c1-28)"
test "${#random}" -eq 28
printf 'qbt_%s\n' "$random" >"$workdir/api-key"
unset random
api_key="$(cat "$workdir/api-key")"
cat >"$workdir/qBittorrent.conf" <<EOF
[BitTorrent]
Session\Port=6881
Session\UseRandomPort=false

[LegalNotice]
Accepted=true

[Network]
PortForwardingEnabled=false

[Preferences]
Connection\PortRangeMin=6881
Connection\UPnP=false
WebUI\Address=127.0.0.1
WebUI\APIKey=$api_key
WebUI\Port=8080
EOF
kubectl apply -f examples/qbittorrent/namespace.yaml
kubectl -n waycloak-qbittorrent create secret generic qbittorrent-adapter-auth \
  --from-file=api-key="$workdir/api-key" \
  --from-file=qBittorrent.conf="$workdir/qBittorrent.conf"
unset api_key
```

The checked-in base deliberately contains a non-pullable, digest-shaped adapter
placeholder. Copy it and replace that placeholder with the signed immutable
reference from the release manifest before rendering:

```sh
cp -R examples/qbittorrent ./qbittorrent-waycloak
cd ./qbittorrent-waycloak
adapter_image="$(jq -er '.artifacts.qbittorrentAdapterImage.reference | select(test("@sha256:[a-f0-9]{64}$"))' /path/to/release-manifest.json)"
kustomize edit set image waycloak.invalid/qbittorrent-adapter="$adapter_image"
kubectl kustomize .
kubectl apply -k .
```

The rendered adapter image uses the release's signed digest reference, has no
Kubernetes token or Linux capabilities, talks only to qBitTorrent and the
Waycloak agent over Pod loopback, and acknowledges the exact applied lease
generation. Pin the image by digest from the signed release manifest for a
production deployment.

Its readiness probe remains ready across at most two consecutive qBitTorrent
control-API observation failures, bounded to 15 seconds from the first
failure, when the already verified lease identity, generation, and application
port are unchanged. It recovers immediately on the next successful
observation. Lease loss, expiry, rotation, listener loss, application failure,
or acknowledgement failure withdraws readiness immediately, so hysteresis
cannot preserve an endpoint for stale provider state.

Published releases also attach `qbittorrent-example.yaml`, rendered by the tag
workflow with the exact adapter reference stored in the signed release
manifest. Verify the release manifest, the release-file provenance, and that
reference before applying the asset; the checked-in Kustomize base remains the
version-independent source for customization.

Check the observed path with:

```sh
kubectl -n waycloak-qbittorrent get portforwardlease qbittorrent -o yaml
kubectl -n waycloak-qbittorrent get pod -l app.kubernetes.io/name=qbittorrent
```

Treat `Ready=True` as Waycloak data-plane evidence, not proof of healthy DHT.
The release test proves exact tracker advertisement and listener rotation with
no Pod restart; production acceptance must additionally verify real-provider
TCP and UDP peer ingress, healthy DHT nodes, and sustained renewal across at
least one provider rotation.
