# Getting started

This guide takes a new cluster from no Waycloak installation to one disposable
workload using a shared Proton/OpenVPN gateway. It deliberately starts with
private egress only. Add provider port forwarding after the basic path is
healthy.

## Before you begin

You need:

- a Kubernetes cluster with Linux workers;
- Kubernetes 1.35 or 1.36 for the currently verified compatibility set;
- Kindnet or Flannel for the currently verified CNI set;
- `kubectl`, Helm 3.14 or newer, `curl`, `jq`, and Cosign 2.6 or newer;
- cluster-admin access for CRDs, admission webhooks, and cluster-scoped RBAC;
- worker kernels with TUN, VXLAN, connection tracking, and nftables support;
- a CNI or host firewall that permits UDP 4789 between protected workload
  nodes and gateway nodes;
- Proton VPN OpenVPN credentials, stored in files outside the repository;
- either an existing cert-manager installation or an externally managed
  webhook serving certificate.

Waycloak v0.2.0 is pre-1.0 software. Read the
[current release boundaries](../README.md#current-release),
[security exceptions](operations/security-exceptions.md), and
[threat model](security/threat-model.md) before using it for sensitive
workloads.

## 1. Verify and download the release

The release manifest is the source of truth for artifact identities. Do not
copy a mutable image tag from a registry page.

```sh
version=v0.2.0
release_url="https://github.com/Amoenus/waycloak/releases/download/$version"

curl --fail --location --remote-name "$release_url/release-manifest.json"
curl --fail --location --remote-name "$release_url/release-manifest.sigstore.json"

identity="https://github.com/Amoenus/waycloak/.github/workflows/release.yaml@refs/tags/$version"
issuer="https://token.actions.githubusercontent.com"

cosign verify-blob \
  --bundle release-manifest.sigstore.json \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  release-manifest.json

jq -e '.version == "0.2.0"' release-manifest.json >/dev/null

jq -r '.artifacts[] | .reference' release-manifest.json |
while IFS= read -r artifact; do
  cosign verify \
    --certificate-identity "$identity" \
    --certificate-oidc-issuer "$issuer" \
    "$artifact" >/dev/null
done
```

Pull the chart recorded in the manifest and verify Helm resolved the same
digest:

```sh
chart_ref="$(jq -r '.artifacts.helmChart.reference' release-manifest.json)"
chart_name="${chart_ref%@*}"
expected_digest="${chart_ref##*@}"

pull_output="$(helm pull "oci://$chart_name" --version "${version#v}" 2>&1)"
printf '%s\n' "$pull_output"
actual_digest="$(printf '%s\n' "$pull_output" | awk '$1 == "Digest:" {print $2}')"
test "$actual_digest" = "$expected_digest"
test -f "waycloak-${version#v}.tgz"
```

## 2. Prepare webhook TLS

Choose one mode.

### Existing cert-manager installation

If cert-manager and its CA injector already run in the cluster, the chart can
create a namespaced self-signed issuer and serving certificate:

```sh
tls_arguments="--set webhook.tls.certManager.enabled=true"
```

This is optional integration, not a Waycloak runtime dependency. The command
does not install cert-manager.

### Externally managed certificate

If cert-manager is not installed, create a `kubernetes.io/tls` Secret whose
certificate is valid for `waycloak-webhook.waycloak-system.svc`, and obtain the
matching base64-encoded CA bundle. The complete OpenSSL procedure is in
[Install Waycloak](operations/install.md#existing-secret-and-static-ca).

```sh
tls_arguments="--set webhook.tls.existingSecret=waycloak-webhook-tls --set-string webhook.tls.caBundle=$ca_bundle"
```

Never pass the CA private key to Helm or commit it to a repository.

## 3. Install the control plane

```sh
# shellcheck disable=SC2086 # tls_arguments is an intentional argument list
helm upgrade --install waycloak "waycloak-${version#v}.tgz" \
  --namespace waycloak-system \
  --create-namespace \
  $tls_arguments \
  --wait --timeout 5m
```

Verify the installation:

```sh
kubectl wait --for=condition=Available deployment/waycloak \
  --namespace waycloak-system --timeout=5m
kubectl get pods --namespace waycloak-system
kubectl get crd \
  vpngateways.networking.waycloak.io \
  vpnworkloads.networking.waycloak.io \
  portforwardleases.networking.waycloak.io
kubectl get mutatingwebhookconfiguration,validatingwebhookconfiguration \
  -l app.kubernetes.io/name=waycloak
```

There should be two ready controller replicas and three installed CRDs.

## 4. Identify cluster-local CIDRs

In `Preserve` mode, Kubernetes Pod and Service traffic stays on the CNI path
while external traffic uses the VPN. Waycloak intentionally does not guess
these trust boundaries using broad node RBAC.

List the Pod CIDRs Kubernetes exposes:

```sh
kubectl get nodes \
  -o jsonpath='{range .items[*]}{.spec.podCIDR}{"\n"}{end}' |
  sort -u
```

Obtain the Service CIDR from your cluster installation or distribution
configuration. Common defaults such as `10.42.0.0/16` and `10.43.0.0/16` are
examples, not safe universal values.

Choose an overlay CIDR that overlaps none of the following:

- node, Pod, or Service CIDRs;
- the VPN provider's tunnel range;
- networks applications must reach directly.

The examples below use `172.30.99.0/24`; change it if that range overlaps your
environment.

## 5. Create the gateway namespace and credentials

Gateway Pods require an explicit Pod Security exception. Keep the gateway in
a dedicated namespace and tightly restrict who can create workloads there.

```sh
kubectl create namespace waycloak-egress
kubectl label namespace waycloak-egress \
  pod-security.kubernetes.io/enforce=privileged \
  pod-security.kubernetes.io/warn=restricted \
  pod-security.kubernetes.io/audit=restricted
```

Create the provider Secret from files so values do not enter shell history:

```sh
kubectl create secret generic vpn-credentials \
  --namespace waycloak-egress \
  --from-file=username=/secure/path/openvpn-username \
  --from-file=password=/secure/path/openvpn-password
```

The Secret must contain keys named `username` and `password`. Waycloak mounts
it only into the VPN engine. It is never copied to the controller, protected
workloads, status, or events. An external secret operator may create the same
ordinary Kubernetes Secret; Waycloak has no dependency on that operator.

## 6. Create a gateway

Save the following as `gateway.yaml`. Replace the two example cluster CIDRs
with the values from your cluster before applying it.

```yaml
apiVersion: networking.waycloak.io/v1alpha1
kind: VPNGateway
metadata:
  name: proton-eu
  namespace: waycloak-egress
spec:
  engine:
    type: Gluetun
    image: docker.io/qmcgaw/gluetun:v3.41.0@sha256:6b54856716d0de56e5bb00a77029b0adea57284cf5a466f23aad5979257d3045
  provider:
    name: protonvpn
    protocol: openvpn
    region: Netherlands
    credentialsSecretRef:
      name: vpn-credentials
  overlay:
    cidr: 172.30.99.0/24
    vni: 7999
    mtu: 1320
  dns:
    mode: Gateway
  clusterTraffic:
    mode: Preserve
    cidrs:
      - 10.42.0.0/16 # replace with your Pod CIDR or CIDRs
      - 10.43.0.0/16 # replace with your Service CIDR
  portForwarding:
    enabled: false
  workloadAccess:
    namespaceSelector:
      matchLabels:
        networking.waycloak.io/access: allowed
```

```sh
kubectl apply -f gateway.yaml
kubectl wait --for=condition=Ready vpngateway/proton-eu \
  --namespace waycloak-egress --timeout=5m
kubectl get vpngateway/proton-eu --namespace waycloak-egress
```

If it does not become ready, inspect its conditions in order rather than
decoding the credential Secret:

```sh
kubectl describe vpngateway/proton-eu --namespace waycloak-egress
kubectl get events --namespace waycloak-egress --sort-by=.lastTimestamp
```

## 7. Protect a disposable workload

The repository includes a minimal curl Deployment. Its namespace is dedicated
to the example because Pod Security Admission evaluates the injected
`NET_ADMIN` container as part of the whole Pod.

```sh
kubectl apply -k examples/curl
kubectl wait --for=condition=Available deployment/waycloak-demo \
  --namespace waycloak-demo --timeout=5m
```

If your gateway has a different namespace or name, edit the annotation in
`examples/curl/deployment.yaml` before applying it.

Inspect what admission added:

```sh
kubectl get pod --namespace waycloak-demo \
  -l app.kubernetes.io/name=waycloak-demo \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n  init: "}{.spec.initContainers[*].name}{"\n  containers: "}{.spec.containers[*].name}{"\n"}{end}'
kubectl get vpnworkloads --namespace waycloak-demo
```

You should see `waycloak-prepare`, `waycloak-verify`, the original `curl`
container, and `waycloak-agent`.

Verify protected DNS and HTTPS without printing the VPN exit address:

```sh
kubectl exec --namespace waycloak-demo deployment/waycloak-demo \
  --container curl -- \
  curl --fail --silent --show-error --output /dev/null \
  --write-out 'protected HTTPS status: %{http_code}\n' \
  https://example.com
```

To compare exit addresses, use an IP-check service from both an ordinary Pod
and the protected Pod, but treat the resulting addresses as sensitive
operational metadata and do not paste them into public issue reports.

## 8. Understand the result

You authored:

- one `VPNGateway` and one credentials Secret;
- a namespace authorization label;
- one annotation on the workload Pod template.

Waycloak created or injected:

- the gateway StatefulSet, Service, desired-state ConfigMap, and disruption
  budget;
- a controller-owned `VPNWorkload` and UID-bound allocation ConfigMap;
- fail-closed init containers and the long-running workload agent;
- the VXLAN overlay, protected routes, DNS redirection, and owned firewall
  state.

Continue with
[Architecture and ownership](concepts/architecture-and-ownership.md) before
adapting a production workload.

## 9. Clean up the example

```sh
kubectl delete namespace waycloak-demo
```

Keep the gateway if another workload will use it. To remove Waycloak entirely,
follow the ordered [uninstall guide](operations/uninstall.md); uninstalling the
controller before migrating protected Pods can intentionally leave them
without egress.

## Add port forwarding later

Do not enable provider port forwarding until private egress is healthy. Proton
NAT-PMP requires a port-forward-capable server and an OpenVPN username with
Proton's required `+pmp` suffix. Then enable `ProtonNatPmp` on the gateway and
create a `PortForwardLease`.

Applications that listen on a fixed port require no application adapter.
Applications that advertise their external port inside a protocol may require
a narrow adapter. The supported qBitTorrent exception and its additional
Secret/configuration are documented in
[the qBitTorrent example](../examples/qbittorrent/README.md).
