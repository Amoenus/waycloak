# qBitTorrent example

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
  must use qBitTorrent's `qbt_` prefix followed by 28 hexadecimal characters.
  Store the same key in `WebUI\APIKey` in `qBittorrent.conf`, bind the Web UI to
  loopback, and disable qBitTorrent UPnP/NAT-PMP. For example:

```sh
api_key="qbt_$(openssl rand -hex 14)"
cat >qBittorrent.conf <<EOF
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
kubectl -n waycloak-qbittorrent create secret generic qbittorrent-adapter-auth \
  --from-literal=api-key="$api_key" \
  --from-file=qBittorrent.conf
rm qBittorrent.conf
unset api_key
```

Render and apply with:

```sh
kubectl kustomize examples/qbittorrent
kubectl apply -k examples/qbittorrent
```

The adapter image uses the release's immutable semantic version, has no
Kubernetes token or Linux capabilities, talks only to qBitTorrent and the
Waycloak agent over Pod loopback, and acknowledges the exact applied lease
generation. Pin the image by digest from the signed release manifest for a
production deployment.

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
