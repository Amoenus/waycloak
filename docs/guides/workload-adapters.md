# Workload adapters

A `WorkloadAdapter` is a last-resort application integration. It does not create
a VPN tunnel, acquire a provider mapping, or program packet rules. It applies
and observes one already-authorized lease generation in an application that
cannot operate correctly with Waycloak's default fixed-port translation.

Prefer these options in order:

1. a fixed application port with Waycloak translation;
2. an application-supported standard or neutral lease interface; and
3. an explicitly trusted `WorkloadAdapter` only when the application must learn
   or advertise the provider-assigned port.

The stable release qualifies qBittorrent `5.2.3` through its evidence-backed
reference adapter because torrent clients advertise their listening port. The
qBittorrent application itself is not shipped by Waycloak. The integration uses qBittorrent's
official [WebUI API](https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-%28qBittorrent-5.0%29).

## Adapter security contract

Every adapter deployment must satisfy all of these rules:

- one unprivileged container and exactly one TCP container port, `9444`;
- an immutable `repository@sha256:digest` matching the `WorkloadAdapter` spec;
- no service-account token, host namespace, host path, init container, added
  Linux capability, privilege escalation, or writable root filesystem;
- TLS 1.3 mutual authentication for the exact gateway-runtime and controller
  SPIFFE client identities;
- durable non-secret generation state across Pod replacement;
- application credentials owned by the application namespace, never VPN or
  Kubernetes credentials; and
- one deterministic Service resolving to exactly one ready adapter Pod.

Adapter failure withdraws lease readiness. It cannot permit ordinary egress or
change the protected workload's route.

## qBittorrent prerequisites

Complete the advanced [port-forward installation](../advanced-setup.md#port-forwarding),
including `--enable-port-forwarding` and `--enable-adapter-protocol`. The
gateway requests both optional capabilities:

```yaml
requestedFeatures:
  - networking.waycloak.io/PortForwardServiceSingleActive
  - networking.waycloak.io/WorkloadAdapter
```

The route uses the same names under `requiredFeatures`:

```yaml
requiredFeatures:
  - networking.waycloak.io/PortForwardServiceSingleActive
  - networking.waycloak.io/WorkloadAdapter
```

Configure qBittorrent through its own documented configuration surface:

- one replica selected by a stable Kubernetes Service;
- the Waycloak route label on its Pod template;
- peer TCP and UDP listeners using the same initial/fallback port;
- DHT enabled so the adapter can prove the UDP listener with a BEP 5 ping;
- WebUI API v2 over HTTPS on Pod port `8080` and reachable from the adapter;
- a server certificate valid for the configured WebUI server name; and
- a dedicated WebUI username/password stored in a Secret.

qBittorrent owns its WebUI, TLS, authentication, DHT, torrent, and reannounce
behavior. Follow the [qBittorrent wiki](https://github.com/qbittorrent/qBittorrent/wiki/)
and [WebUI API documentation](https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-%28qBittorrent-5.0%29).
The upstream [WebUI HTTPS guide](https://github.com/qbittorrent/qBittorrent/wiki/Linux-WebUI-setting-up-HTTPS-with-self-signed-SSL-certificates)
describes qBittorrent's certificate settings; use a CA-issued certificate with
the service name required below rather than copying its self-signed example.
Do not enable local-auth bypass. The Waycloak adapter logs in with the dedicated
credentials, verifies a protected preferences read, changes only the listener
settings, observes TCP and UDP, and reannounces active torrents.

## 1. Create the qBittorrent backend Service

This example assumes the qBittorrent Pod has `app: qbittorrent` and both peer
protocols use container port `51413` initially. A numeric lease backend
reference selects both same-number Service ports.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: qbittorrent-peer
  namespace: media
spec:
  selector:
    app: qbittorrent
  ports:
    - name: peer-tcp
      port: 51413
      targetPort: 51413
      protocol: TCP
    - name: peer-udp
      port: 51413
      targetPort: 51413
      protocol: UDP
```

Keep one ready endpoint. `SingleActive` binds the lease to one exact Pod UID;
Waycloak does not load-balance one provider mapping across application Pods.

## 2. Prepare adapter identity and application inputs

For adapter name `qbittorrent` in namespace `media`, the deterministic Service
name is `waycloak-adapter-7a5205a2e48d8bc4`. It is the prefix
`waycloak-adapter-` plus the first 16 hexadecimal characters of SHA-256 over
`media/qbittorrent`.

Before applying the workload below, create these namespace-local inputs from
your PKI and credential sources:

| Object | Required data | Purpose |
| --- | --- | --- |
| Secret `qbittorrent-adapter-mtls` | `tls.crt`, `tls.key`, `ca.crt` | adapter server identity and CA for exact controller/runtime clients |
| ConfigMap `qbittorrent-webui-ca` | `ca.crt` | public CA that issued qBittorrent's WebUI certificate |
| Secret `qbittorrent-webui-auth` | `username`, `password` | dedicated qBittorrent WebUI account |

For the default cluster domain, the adapter serving certificate must cover
`waycloak-adapter-7a5205a2e48d8bc4.media.svc.cluster.local`. Replace only the
domain suffix when preflight reports a different cluster domain. Its client CA
must validate only the release-configured controller and gateway-runtime client
certificates, whose URI identities are:

```text
spiffe://waycloak.io/replacement-controller
spiffe://waycloak.io/gateway-runtime
```

The qBittorrent WebUI certificate in this example covers
`qbittorrent-peer.media.svc.cluster.local`. Only its public CA is copied to the
adapter; the qBittorrent TLS private key remains with qBittorrent.

## 3. Deploy and trust the reference adapter

The following example uses the exact qBittorrent adapter image published in
Waycloak `v1.0.1`. Change namespace or adapter name only after recalculating the
deterministic Service name and issuing a matching certificate.

The same resources are available as the directly reusable
[`docs/examples/qbittorrent-adapter.yaml`](../examples/qbittorrent-adapter.yaml)
example.

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: qbittorrent-adapter-state
  namespace: media
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 32Mi
---
apiVersion: v1
kind: Service
metadata:
  name: waycloak-adapter-7a5205a2e48d8bc4
  namespace: media
spec:
  selector:
    app: waycloak-qbittorrent-adapter
  ports:
    - name: https
      port: 9444
      targetPort: 9444
      protocol: TCP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: waycloak-qbittorrent-adapter
  namespace: media
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: waycloak-qbittorrent-adapter
  template:
    metadata:
      labels:
        app: waycloak-qbittorrent-adapter
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        fsGroupChangePolicy: OnRootMismatch
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: adapter
          image: ghcr.io/amoenus/waycloak-qbittorrent-adapter@sha256:327ff7dd23a0ab5296d00001376419df419774d93a15672ae112e4d71bfe20c0
          args:
            - --listen-address=:9444
            - --tls-cert=/var/run/adapter-tls/tls.crt
            - --tls-key=/var/run/adapter-tls/tls.key
            - --client-ca=/var/run/adapter-tls/ca.crt
            - --namespace=media
            - --name=qbittorrent
            - --image=ghcr.io/amoenus/waycloak-qbittorrent-adapter@sha256:327ff7dd23a0ab5296d00001376419df419774d93a15672ae112e4d71bfe20c0
            - --pod-uid=$(POD_UID)
            - --state-file=/var/lib/waycloak/adapter-state.json
            - --qbittorrent-ca=/var/run/qbittorrent-ca/ca.crt
            - --qbittorrent-server-name=qbittorrent-peer.media.svc.cluster.local
            - --qbittorrent-username-file=/var/run/qbittorrent-auth/username
            - --qbittorrent-password-file=/var/run/qbittorrent-auth/password
            - --qbittorrent-port=8080
          env:
            - name: POD_UID
              valueFrom:
                fieldRef:
                  fieldPath: metadata.uid
          ports:
            - name: https
              containerPort: 9444
              protocol: TCP
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
            readOnlyRootFilesystem: true
            runAsNonRoot: true
          volumeMounts:
            - name: adapter-tls
              mountPath: /var/run/adapter-tls
              readOnly: true
            - name: qbittorrent-ca
              mountPath: /var/run/qbittorrent-ca
              readOnly: true
            - name: qbittorrent-auth
              mountPath: /var/run/qbittorrent-auth
              readOnly: true
            - name: state
              mountPath: /var/lib/waycloak
      volumes:
        - name: adapter-tls
          secret:
            secretName: qbittorrent-adapter-mtls
        - name: qbittorrent-ca
          configMap:
            name: qbittorrent-webui-ca
        - name: qbittorrent-auth
          secret:
            secretName: qbittorrent-webui-auth
        - name: state
          persistentVolumeClaim:
            claimName: qbittorrent-adapter-state
---
apiVersion: networking.waycloak.io/v1beta1
kind: WorkloadAdapter
metadata:
  name: qbittorrent
  namespace: media
spec:
  image: ghcr.io/amoenus/waycloak-qbittorrent-adapter@sha256:327ff7dd23a0ab5296d00001376419df419774d93a15672ae112e4d71bfe20c0
  protocolVersion: networking.waycloak.io/adapter/v1
  supportedApplications:
    - networking.waycloak.io/qbittorrent
  supportedFeatures:
    - networking.waycloak.io/PortForwardServiceSingleActive
```

The PVC contains only non-secret lease identity and generation state. Back it
with storage that survives Pod replacement. The controller rejects an adapter
Pod that violates the security contract or does not use the exact trusted image.

Wait for the adapter protocol observation:

```sh
kubectl -n media wait \
  --for=condition=Ready workloadadapter/qbittorrent --timeout=2m
kubectl -n media get workloadadapter/qbittorrent -o wide
```

## 4. Bind the lease to qBittorrent

```yaml
apiVersion: networking.waycloak.io/v1beta1
kind: PortForwardLease
metadata:
  name: qbittorrent-peer
  namespace: media
spec:
  gatewayRef:
    name: private
    namespace: media
  backendRef:
    group: ""
    kind: Service
    name: qbittorrent-peer
    port: 51413
  protocols: [TCP, UDP]
  endpointPolicy: SingleActive
  applicationAdapterRef:
    name: qbittorrent
```

Apply the lease and require every applicable current-generation condition to be
healthy:

```sh
kubectl -n media wait \
  --for=condition=Ready portforwardlease/qbittorrent-peer --timeout=5m
kubectl -n media describe portforwardlease/qbittorrent-peer
```

Readiness means the provider mapping, gateway rules, exact Service/Pod target,
delivery, qBittorrent listener settings, TCP listener, UDP DHT response, and
application acknowledgement were all freshly observed. It does not merely mean
the adapter accepted a request.

## KCL trust record

After adding the stable KCL module, the immutable trust record can be authored
as:

```python
import waycloak.v1beta1 as networking

adapter = networking.WorkloadAdapter {
    metadata = {name = "qbittorrent", namespace = "media"}
    spec = {
        image = "ghcr.io/amoenus/waycloak-qbittorrent-adapter@sha256:327ff7dd23a0ab5296d00001376419df419774d93a15672ae112e4d71bfe20c0"
        protocolVersion = "networking.waycloak.io/adapter/v1"
        supportedApplications = ["networking.waycloak.io/qbittorrent"]
        supportedFeatures = ["networking.waycloak.io/PortForwardServiceSingleActive"]
    }
}

items = [adapter]
```

The adapter Deployment, Service, PKI, credentials, PVC, qBittorrent workload,
and lease remain ordinary Kubernetes configuration alongside this typed trust
record.

## Rotation and failure behavior

`WorkloadAdapter.spec` is immutable. When a release changes the adapter digest:

1. commit the new Deployment image and matching trust-record image together;
2. complete the reviewed `waycloakctl` release transition;
3. delete and recreate only the exact old `WorkloadAdapter` UID through the
   documented GitOps handoff; and
4. wait for adapter and lease readiness before considering inbound service
   restored.

During adapter, qBittorrent API, TLS, authentication, listener, or storage
failure, the lease becomes unready and inbound rules are withdrawn. Protected
outbound traffic remains subject to its route and never falls back to ordinary
egress.

## Troubleshooting

```sh
kubectl -n media describe workloadadapter qbittorrent
kubectl -n media get service waycloak-adapter-7a5205a2e48d8bc4
kubectl -n media get endpointslice \
  -l kubernetes.io/service-name=waycloak-adapter-7a5205a2e48d8bc4
kubectl -n media logs deployment/waycloak-qbittorrent-adapter
kubectl -n media describe portforwardlease qbittorrent-peer
```

Common causes are a wrong deterministic Service name, more than one adapter or
qBittorrent endpoint, an image mismatch, invalid client SPIFFE identity, an
incorrect WebUI certificate name, failed WebUI authentication, disabled DHT,
or a listener that does not match the current lease generation. Do not solve
these failures by disabling TLS/authentication, adding capabilities, exposing
Gluetun's control server, or removing workload enrollment.

For the language-neutral protocol, see
[Workload adapter protocol v1](../api/workload-adapter-v1.md).
