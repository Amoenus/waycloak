# Getting started with Waycloak v0.1.0-rc.30

Waycloak routes explicitly enrolled Kubernetes Pods through a shared
Proton/OpenVPN gateway and fails closed when that protected path is not ready.
This release candidate is feature complete and freezes the
`networking.waycloak.io/v1beta1` contract. It is not the final stable release.

After verifying the signed release assets, use `waycloakctl` for installation.
The command validates the manifest's canonical exact-artifact identity,
observes the cluster, generates a reviewable plan, and applies that plan only
after the plan ID is confirmed. Do not translate these steps into a mutable
`helm install` command: the generated plan owns the CNI paths, release
identity, image digests, observation trust, and safe activation sequence.

## Supported quick path

RC.30 carries one certified operator support row:

| Kubernetes | Distribution/CNI | Runtime | Kernel | Architecture | VPN engine/configuration |
| --- | --- | --- | --- | --- | --- |
| `v1.36.1+k3s1` | K3s with Flannel | `containerd://2.2.3-k3s1` | Linux 5.10 or newer | `amd64` | Gluetun with Proton/OpenVPN |

That row covers protected TCP and UDP egress, contained DNS, single-active
provider port forwarding, and the optional workload-adapter handoff. The exact
machine-readable claim is in `release-manifest.json`. Kind, k3d, `arm64`, and
the multi-platform image indexes provide development, CI, or artifact-availability
evidence; they are not additional certified operator rows in this RC.

The certified quick path requires:

- Kubernetes `v1.36.1+k3s1` with stable validating and mutating admission policy
  APIs and the NodeRestriction admission plugin;
- Linux `amd64` nodes using kernel 5.10 or newer;
- K3s Flannel with `containerd://2.2.3-k3s1`;
- CoreDNS with one observable Service address and cluster domain;
- an unused private IPv4 overlay CIDR between `/16` and `/29` that does not
  overlap Pod, Service, node, LAN, or VPN networks;
- permission to install cluster-scoped CRDs, admission policies, RBAC, a
  privileged node-agent DaemonSet, and a chained CNI binary/configuration;
- Proton OpenVPN credentials stored in a same-namespace Kubernetes Secret.

Run preflight before creating any Waycloak resources. An incompatible result is
a hard stop with remediation; it is not an invitation to bypass the check.

## 1. Download and verify the release

Set the RC tag and download the complete release. Choose the CLI binary for your
operating system and architecture.

```sh
export WAYCLOAK_TAG=v0.1.0-rc.30
gh release download "$WAYCLOAK_TAG" \
  --repo Amoenus/waycloak \
  --dir "waycloak-${WAYCLOAK_TAG}"
cd "waycloak-${WAYCLOAK_TAG}"
sha256sum --check SHA256SUMS
```

Verify the signed runtime release inventory:

```sh
cosign verify-blob \
  --bundle release-manifest.sigstore.json \
  --certificate-identity "https://github.com/Amoenus/waycloak/.github/workflows/waycloak-release.yaml@refs/tags/${WAYCLOAK_TAG}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  release-manifest.json
```

Verify the separately published CLI checksum inventory:

```sh
sha256sum --check waycloakctl-SHA256SUMS
cosign verify-blob \
  --bundle waycloakctl-SHA256SUMS.sigstore.json \
  --certificate-identity "https://github.com/Amoenus/waycloak/.github/workflows/waycloakctl-release.yaml@refs/tags/${WAYCLOAK_TAG}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  waycloakctl-SHA256SUMS
```

The runtime and CLI workflows publish uniquely named checksum inventories into
the same GitHub release. GitHub provenance can also be checked with `gh
attestation verify`; every OCI reference is recorded by digest in
`release-manifest.json`.

Install the CLI locally and confirm its embedded version:

```sh
install -m 0755 waycloakctl-linux-amd64 /usr/local/bin/waycloakctl
waycloakctl version
```

## 2. Preflight and install

Choose an overlay that is private and unused in your environment:

```sh
export KUBE_CONTEXT=my-cluster
export WAYCLOAK_OVERLAY=100.96.0.0/16

waycloakctl preflight \
  --context "$KUBE_CONTEXT" \
  --overlay-cidr "$WAYCLOAK_OVERLAY" \
  --output human
```

Do not continue unless `Compatible: true`. On a mixed-architecture cluster,
explicitly add `--node-architecture amd64` to the next command. That choice
limits both privileged DaemonSets to the certified nodes. A successful
preflight on another compatible layout is useful evaluation evidence but does
not expand the release's certified support matrix.

```sh
waycloakctl install plan \
  --context "$KUBE_CONTEXT" \
  --release-manifest release-manifest.json \
  --overlay-cidr "$WAYCLOAK_OVERLAY" \
  --namespace waycloak-system \
  --output json >install-plan.json
```

Review `install-plan.json`, especially `securityChanges`, `cniChanges`,
`nodeArchitecture`, `valuesYAML`, and `rollback`. Then apply the exact plan ID:

```sh
PLAN_ID="$(jq -r .planID install-plan.json)"
waycloakctl install apply \
  --context "$KUBE_CONTEXT" \
  --plan install-plan.json \
  --confirm "$PLAN_ID"

waycloakctl doctor --context "$KUBE_CONTEXT" --output human
```

`doctor` must report healthy controller, CNI, selected-node capability, and
release identity before a workload is enrolled.

## 3. Create a gateway

This quick path supports the reviewed Proton/OpenVPN recipe. Create the workload
namespace and a Secret with exactly `username` and `password` keys. Use your
credential source of truth; do not place values in Git, shell history, Helm
values, or a Waycloak manifest.

```sh
kubectl --context "$KUBE_CONTEXT" create namespace media
kubectl --context "$KUBE_CONTEXT" -n media create secret generic proton-credentials \
  --from-file=username=/secure/path/proton-openvpn-username \
  --from-file=password=/secure/path/proton-openvpn-password
```

Grant only the Waycloak controller in `waycloak-system` permission to read
gateway credentials in this namespace:

```sh
kubectl --context "$KUBE_CONTEXT" -n media create rolebinding waycloak-gateway-secret-reader \
  --clusterrole=waycloak-gateway-secret-reader \
  --serviceaccount=waycloak-system:waycloak
```

Render and apply the non-secret engine configuration and gateway intent:

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

Gluetun owns VPN-provider support in this path; Waycloak does not require a
provider plugin or application plugin for ordinary protected TCP, UDP, or DNS.
Optional inbound port forwarding also uses a stable Service port without an
application adapter by default. Select a `WorkloadAdapter` only when documented
application behavior requires changing or advertising the provider-assigned
port; qBittorrent is the reference exception.

## 4. Create a route and enroll a workload

Create a same-namespace route:

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

Apply it, then put exactly one enrollment label on the Pod template—not on an
already-created Pod:

```yaml
spec:
  template:
    metadata:
      labels:
        networking.waycloak.io/egress-route: private
```

The label is fail-closed intent. If the route, gateway, agent, tunnel, or DNS
path is unavailable, the application container is denied startup or its egress
remains blocked. Removing or changing enrollment requires a new Pod.

Confirm the route and generated binding:

```sh
waycloakctl doctor \
  --context "$KUBE_CONTEXT" \
  --namespace media \
  --route private \
  --output human

kubectl --context "$KUBE_CONTEXT" -n media get \
  vpngateway,vpnegressroute,vpnworkloadbinding
```

## Helm and OCI consumption

The chart is available both as the release asset
`waycloak-0.1.0-rc.30.tgz` and as
`oci://ghcr.io/amoenus/charts/waycloak:0.1.0-rc.30`. Its exact digest is in
`waycloak-chart.ref` and `release-manifest.json`:

```sh
helm pull oci://ghcr.io/amoenus/charts/waycloak \
  --version 0.1.0-rc.30
```

Use Helm directly for inspection, rendering, and GitOps consumption after a
reviewed `waycloakctl` transition. Do not use raw Helm as a replacement for the
initial or changed-release transaction.

If the release changes a selected workload-adapter digest, its
`WorkloadAdapter` trust record must be replaced rather than patched because its
spec is immutable. Commit the trust record and matching adapter workload image
together, complete the `waycloakctl` release transition, then perform the
reviewed exact-UID delete/recreate transaction through GitOps and verify adapter
and lease readiness. The exact controller-neutral procedure and Argo CD caveat are in
[Turnkey bootstrap and lifecycle](implementation/turnkey-bootstrap.md#rotate-immutable-workload-adapter-trust-records).

All runtime images are multi-platform OCI indexes. Consume their immutable
`repository@sha256:digest` identities from `release-manifest.json`; mutable
`latest` references are neither published nor supported.

## KCL consumption

KCL is optional and has no runtime role. The RC publishes the generated schema
module as signed OCI artifact
`oci://ghcr.io/amoenus/waycloak-kcl:0.1.0-rc.30` and as the downloadable
`waycloak-kcl-v0.1.0-rc.30.tar`. Verify its exact digest in `waycloak-kcl.ref`.
Add the OCI module to an existing KCL package, then import its schemas:

```sh
kcl mod add oci://ghcr.io/amoenus/waycloak-kcl --tag 0.1.0-rc.30
```

```python
import waycloak.v1beta1 as networking

route = networking.VPNEgressRoute {
    metadata = {name = "private", namespace = "media"}
    spec.parentRefs = [{name = "private", namespace = "media"}]
}
```

For a ready-to-render example, extract the downloadable module archive and run:

```sh
kcl run examples/private-egress.k -S items
```

The module supplies schemas and authoring examples only. Install Waycloak with
the signed Helm/CLI transaction before applying KCL-rendered resources.

## Next steps and safety boundaries

- [Use cases](use-cases.md)
- [Configuration requirements](configuration.md)
- [Deployable resources and ownership](deployable-resources.md)
- [Generated API reference](api/v1beta1.md)
- [Upgrade, rollback, and repair](implementation/turnkey-bootstrap.md)
- [Backup and restore](operations/backup-restore-and-disaster-recovery.md)
- [Release-candidate scope and limitations](releases/v0.1.0-rc.30.md)

Waycloak provides selected, fail-closed VPN egress within its documented threat
model. It does not claim anonymity. Normal Helm uninstall does not delete CRDs
or restore the CNI chain; destructive purge and CNI restoration remain separate
confirmation-gated operations.
