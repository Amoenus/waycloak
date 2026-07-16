# Getting started

Use this quickstart when you want to put a few Pods behind one shared VPN. It
creates a gateway, authorizes one namespace, and protects a disposable curl
Deployment. Port forwarding and production hardening are deliberately left
for later.

This is a concrete Proton/OpenVPN example for the currently verified `v0.2`
path, not Waycloak's generic provider model. Engine-native Gluetun
configuration for its other supported providers and protocols is the next API
slice described by [ADR 0017](decisions/0017-engine-native-configuration-boundary.md).

## What you need

- a Linux Kubernetes cluster running a [supported Kubernetes and CNI
  combination](../README.md#current-release);
- Waycloak installed with Helm; and
- Proton VPN OpenVPN credentials.

If Waycloak is not installed, use the short cert-manager path below or follow
the [detailed installation guide](operations/install.md) for release
verification and externally managed TLS.

```sh
helm upgrade --install waycloak oci://ghcr.io/amoenus/charts/waycloak \
  --version 0.2.1 \
  --namespace waycloak-system \
  --create-namespace \
  --set webhook.tls.certManager.enabled=true \
  --wait --timeout 5m
```

That command uses an existing cert-manager installation; it does not install
cert-manager. The released chart contains the Waycloak CRDs and uses
digest-pinned Waycloak images.

## 1. Create the native VPN configuration Secret

This provider-neutral example uses Gluetun's custom WireGuard file shape. Keep
the native `wg0.conf` in the gateway namespace. Creating the Secret from a file
keeps its private key out of this manifest and your shell history.

```sh
kubectl create namespace waycloak-egress
kubectl create secret generic wireguard-config \
  --namespace waycloak-egress \
  --from-file=wg0.conf=/secure/path/wg0.conf
```

An external secret operator can create the same ordinary Kubernetes Secret.
Waycloak does not depend on that operator, does not read the Secret, and mounts
it only into Gluetun. For built-in providers and OpenVPN shapes, follow the
[Gluetun-native configuration guide](guides/gluetun-native-configuration.md).

## 2. Create a gateway

Save this as `gateway.yaml`. Change the two `clusterTraffic.cidrs` values to
your cluster's Pod and Service CIDRs. The shown values are the common k3s
defaults, not universal defaults.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: waycloak-egress
  labels:
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/warn: restricted
    pod-security.kubernetes.io/audit: restricted
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: gluetun-native
  namespace: waycloak-egress
data:
  VPN_SERVICE_PROVIDER: custom
  VPN_TYPE: wireguard
---
apiVersion: networking.waycloak.io/v1alpha1
kind: VPNGateway
metadata:
  name: private
  namespace: waycloak-egress
spec:
  engine:
    type: Gluetun
    image: docker.io/qmcgaw/gluetun:v3.41.0@sha256:6b54856716d0de56e5bb00a77029b0adea57284cf5a466f23aad5979257d3045
    config:
      envFrom:
        - name: gluetun-native
      files:
        - secretRef:
            name: wireguard-config
          mountPath: /gluetun/wireguard
  overlay:
    cidr: 172.30.99.0/24
    vni: 7999
    mtu: 1320
  dns:
    mode: Gateway
  clusterTraffic:
    mode: Preserve
    cidrs:
      - 10.42.0.0/16 # your Pod CIDR
      - 10.43.0.0/16 # your Service CIDR
  portForwarding:
    enabled: false
  workloadAccess:
    namespaceSelector:
      matchLabels:
        networking.waycloak.io/access: allowed
```

```sh
kubectl apply -f gateway.yaml
kubectl wait --for=condition=Ready vpngateway/private \
  --namespace waycloak-egress --timeout=5m
```

If `172.30.99.0/24` overlaps another network, choose a different private
overlay CIDR. The [advanced configuration guide](operations/advanced-configuration.md)
explains CIDR discovery, cluster-traffic modes, DNS, and overlay planning.

## 3. Protect a workload

Save this as `workload.yaml`. The gateway annotation is the only Waycloak
setting on the workload.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: waycloak-demo
  labels:
    networking.waycloak.io/access: allowed
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/warn: restricted
    pod-security.kubernetes.io/audit: restricted
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: curl
  namespace: waycloak-demo
spec:
  replicas: 1
  selector:
    matchLabels:
      app: curl
  template:
    metadata:
      labels:
        app: curl
      annotations:
        networking.waycloak.io/gateway: waycloak-egress/private
    spec:
      automountServiceAccountToken: false
      containers:
        - name: curl
          image: curlimages/curl:8.16.0@sha256:463eaf6072688fe96ac64fa623fe73e1dbe25d8ad6c34404a669ad3ce1f104b6
          command: [sleep, infinity]
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
            readOnlyRootFilesystem: true
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
```

```sh
kubectl apply -f workload.yaml
kubectl wait --for=condition=Available deployment/curl \
  --namespace waycloak-demo --timeout=5m
kubectl exec --namespace waycloak-demo deployment/curl -- \
  curl --fail --silent --output /dev/null \
  --write-out 'protected HTTPS status: %{http_code}\n' https://example.com
```

Waycloak leaves unannotated Pods unchanged. For the annotated Pod it creates a
stable allocation, waits for that allocation before the application starts,
and injects the networking components that enforce fail-closed VPN egress.
The application receives no VPN credentials, Kubernetes API token, or extra
Linux capabilities.

The complete workload manifest also lives in [`examples/curl`](../examples/curl).
To author the gateway with KCL instead of YAML, use the
[`private-egress.k`](../kcl/waycloak/examples/private-egress.k) example.

## Where to go next

- [Architecture and ownership](concepts/architecture-and-ownership.md) shows
  what you define and what Waycloak creates.
- [Security exceptions](operations/security-exceptions.md) explains why the
  gateway and protected namespaces need their Pod Security labels.
- [Troubleshooting](operations/troubleshooting.md) starts with conditions and
  logs when either wait command fails.
- [Advanced configuration](operations/advanced-configuration.md) covers
  authorization, networking, DNS, KCL, and port forwarding.
- [Verified installation](operations/install.md) covers signatures, immutable
  artifact identities, TLS modes, and production installation.

Remove the disposable workload with `kubectl delete namespace waycloak-demo`.
Keep the gateway for other protected workloads, or follow the ordered
[uninstall guide](operations/uninstall.md).
