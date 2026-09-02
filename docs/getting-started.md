# Getting started with Waycloak v1.0.1

This guide installs the stable release, creates a Proton/OpenVPN gateway, and
enrolls one workload. The result is selected VPN egress that fails closed when
the protected path is unavailable.

## Supported environment

The certified stable support row is:

| Kubernetes | Distribution/CNI | Runtime | Kernel | Architecture | VPN engine/configuration |
| --- | --- | --- | --- | --- | --- |
| `v1.36.1+k3s1` | K3s with Flannel | `containerd://2.2.3-k3s1` | Linux 5.10 or newer | `amd64` | Gluetun with Proton/OpenVPN |

You also need:

- `kubectl`, `jq`, `gh`, `cosign`, and permission to install cluster-scoped
  resources, privileged DaemonSets, and a chained CNI plugin;
- one observable cluster DNS Service and cluster domain;
- an unused private IPv4 overlay CIDR from `/16` through `/29` that does not
  overlap Pod, Service, node, LAN, or VPN networks; and
- Proton OpenVPN credentials stored in a namespaced Kubernetes Secret.

Other environments may be useful for evaluation but are not additional
certified support rows. An incompatible preflight result is a hard stop.

## 1. Download and verify the release

```sh
export WAYCLOAK_TAG=v1.0.1
gh release download "$WAYCLOAK_TAG" \
  --repo Amoenus/waycloak \
  --dir "waycloak-${WAYCLOAK_TAG}"
cd "waycloak-${WAYCLOAK_TAG}"
sha256sum --check SHA256SUMS
```

Verify the signed runtime inventory and the separately published CLI checksum
inventory:

```sh
cosign verify-blob \
  --bundle release-manifest.sigstore.json \
  --certificate-identity "https://github.com/Amoenus/waycloak/.github/workflows/waycloak-release.yaml@refs/tags/${WAYCLOAK_TAG}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  release-manifest.json

sha256sum --check waycloakctl-SHA256SUMS
cosign verify-blob \
  --bundle waycloakctl-SHA256SUMS.sigstore.json \
  --certificate-identity "https://github.com/Amoenus/waycloak/.github/workflows/waycloakctl-release.yaml@refs/tags/${WAYCLOAK_TAG}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  waycloakctl-SHA256SUMS
```

Install the matching CLI for your platform. For Linux `amd64`:

```sh
sudo install -m 0755 waycloakctl-linux-amd64 /usr/local/bin/waycloakctl
waycloakctl version
```

The verified manifest digest for v1.0.1 is
`sha256:f5960ede1d68eafba7765c5dd977bdbc66c1181d38d1c82b882179da589fa7a9`.
Treat a different digest as a verification failure.

## 2. Preflight, plan, and install

```sh
export KUBE_CONTEXT=my-cluster
export WAYCLOAK_OVERLAY=100.96.0.0/16

waycloakctl preflight \
  --context "$KUBE_CONTEXT" \
  --overlay-cidr "$WAYCLOAK_OVERLAY" \
  --output human
```

Continue only when the report says `Compatible: true`:

```sh
waycloakctl install plan \
  --context "$KUBE_CONTEXT" \
  --release-manifest release-manifest.json \
  --overlay-cidr "$WAYCLOAK_OVERLAY" \
  --namespace waycloak-system \
  --node-architecture amd64 \
  --output json >install-plan.json
```

Review `securityChanges`, `cniChanges`, `nodeArchitecture`, `valuesYAML`, and
`rollback`. Then confirm the exact plan:

```sh
PLAN_ID="$(jq -r .planID install-plan.json)"
waycloakctl install apply \
  --context "$KUBE_CONTEXT" \
  --plan install-plan.json \
  --confirm "$PLAN_ID"

waycloakctl doctor --context "$KUBE_CONTEXT" --output human
```

Do not enroll workloads until `doctor` reports healthy controller, CNI,
selected-node capability, and release identity. See the [Helm guide](guides/helm.md)
for chart inspection and GitOps use; raw `helm install` does not replace this
transaction.

## 3. Create a gateway

Create a namespace and credential Secret. Read values from files or your
credential controller; do not put them in Git, Helm values, generated YAML, or
shell arguments.

```sh
kubectl --context "$KUBE_CONTEXT" create namespace media
kubectl --context "$KUBE_CONTEXT" -n media create secret generic proton-credentials \
  --from-file=username=/secure/path/proton-openvpn-username \
  --from-file=password=/secure/path/proton-openvpn-password

kubectl --context "$KUBE_CONTEXT" -n media create rolebinding waycloak-gateway-secret-reader \
  --clusterrole=waycloak-gateway-secret-reader \
  --serviceaccount=waycloak-system:waycloak
```

Generate non-secret Gluetun configuration and gateway intent:

```sh
waycloakctl gateway init \
  --namespace media \
  --name private \
  --class gluetun.waycloak.io \
  --config-map private-gluetun \
  --secret proton-credentials \
  --provider protonvpn \
  --protocol openvpn \
  --overlay-cidr "$WAYCLOAK_OVERLAY" >gateway.yaml

kubectl --context "$KUBE_CONTEXT" apply -f gateway.yaml
kubectl --context "$KUBE_CONTEXT" -n media wait \
  --for=condition=Ready vpngateway/private --timeout=5m
```

`Ready=True` is observed data-plane health, not merely successful registration.

## 4. Create a route and enroll a workload

Save and apply `route.yaml`:

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
      namespace: media
      name: private
  requiredFeatures:
    - networking.waycloak.io/TCP
    - networking.waycloak.io/UDP
    - networking.waycloak.io/DNSContainment
```

```sh
kubectl --context "$KUBE_CONTEXT" apply -f route.yaml
```

Add exactly one label to the controller-owned Pod template of a Deployment,
StatefulSet, Job, or other workload:

```yaml
spec:
  template:
    metadata:
      labels:
        networking.waycloak.io/egress-route: private
```

Apply the workload and inspect the route and controller-created binding:

```sh
waycloakctl doctor \
  --context "$KUBE_CONTEXT" \
  --namespace media \
  --route private \
  --output human

kubectl --context "$KUBE_CONTEXT" -n media get \
  vpngateway,vpnegressroute,vpnworkloadbinding
```

Enrollment changes require a new Pod. Never edit the label directly on a live
Pod. During gateway, tunnel, DNS, agent, or reconfiguration failure, enrolled
traffic remains denied rather than using ordinary egress.

## Continue

- [Configuration reference](configuration.md)
- [Advanced setup](advanced-setup.md)
- [Helm and OCI guide](guides/helm.md)
- [KCL authoring guide](guides/kcl.md)
- [Stable release notes](releases/v1.0.1.md)
