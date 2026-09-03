# GitOps-native platform bootstrap

Waycloak uses one canonical OCI Helm chart. Users choose how to reconcile it:
plain Helm, Flux, or Argo CD. KCL is an optional way to author the small
cluster-owned values overlay and later Waycloak resources; it does not replace
Helm as the package manager.

This clean-install path is implemented on `main` for the certified
K3s/Flannel `amd64` profile and will be available in the first release after
`v1.0.1`. Stable `v1.0.1` predates the chart bootstrap Job and still requires
the documented `waycloakctl install plan/apply` transaction. Do not combine a
new bootstrap asset with the `v1.0.1` chart.

## What is left to the user

The user chooses:

- plain Helm, Flux, or Argo CD;
- an unused overlay CIDR;
- the target cluster and namespace; and
- how VPN and application credentials reach Kubernetes Secrets.

The release supplies:

- a digest-pinned OCI chart;
- exact release and runtime image identities;
- certified K3s/Flannel CNI paths and cluster DNS defaults;
- an idempotent, least-privilege certificate bootstrap Job; and
- controller-first CNI activation inside the chart.

The default `100.96.0.0/16` overlay is only a starting point. Confirm that it
does not overlap Pod, Service, node, LAN, or VPN networks before deployment.

## Release assets

Every supporting release publishes and checksums these generated files:

```text
waycloak-values-k3s-flannel-amd64.yaml
waycloak-flux-k3s-flannel-amd64.yaml
waycloak-argocd-k3s-flannel-amd64.yaml
```

They are deterministically generated from the signed `release-manifest.json`.
The Flux file pins the OCI chart by digest. The Argo CD file pins its exact
chart version because Argo CD's Helm source uses `targetRevision` for OCI
charts. Never substitute `latest`.

## Option 1: plain Helm

Download and verify one exact release as described in
[Getting started](../getting-started.md#1-download-and-verify-the-release).
Create `cluster-values.yaml`:

```yaml
controller:
  gateway:
    overlayCIDR: 100.96.0.0/16
```

Create the privileged infrastructure namespace with ordinary Kubernetes YAML:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: waycloak-system
  labels:
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/audit: privileged
    pod-security.kubernetes.io/warn: privileged
```

Then install the exact chart and the two values layers:

```sh
kubectl apply -f namespace.yaml
helm upgrade --install waycloak \
  oci://ghcr.io/amoenus/charts/waycloak \
  --version "${WAYCLOAK_TAG#v}" \
  --namespace waycloak-system \
  --values waycloak-values-k3s-flannel-amd64.yaml \
  --values cluster-values.yaml \
  --wait --timeout 10m
```

No `waycloakctl` command is required for this clean install.

## Option 2: Flux

Review the overlay CIDR in `waycloak-flux-k3s-flannel-amd64.yaml`, commit that
single generated file, and include it in the relevant Flux `Kustomization`.
It contains only established Flux and Kubernetes resources:

- a privileged `Namespace`;
- a digest-pinned `source.toolkit.fluxcd.io/v1` `OCIRepository`; and
- a `helm.toolkit.fluxcd.io/v2` `HelmRelease` with exact values.

Flux executes the chart's standard Helm pre-install hook and waits for the
release. Refer to the official
[Flux HelmRelease API](https://fluxcd.io/flux/components/helm/api/v2/) and
[OCI chart reference guide](https://fluxcd.io/flux/guides/helmreleases/).

## Option 3: Argo CD

Review the overlay CIDR in `waycloak-argocd-k3s-flannel-amd64.yaml`, commit it,
and apply or include that `Application` in the app-of-apps repository. It uses
an OCI Helm source, an exact `targetRevision`, inline `valuesObject`, automated
sync, and managed namespace Pod Security labels.

Automated prune is disabled because Argo CD renders Helm's `crds/` directory as
managed resources and must not turn deletion of an `Application` into an
implicit destructive Waycloak API purge. Use the separately confirmed removal
procedure when CRDs or host CNI state must be removed.

Argo CD maps supported Helm hooks to its normal sync hooks; no Waycloak plugin
or custom config-management plugin is required. Refer to Argo CD's official
[Helm](https://argo-cd.readthedocs.io/en/stable/user-guide/helm/) documentation.

## Option 4: author the cluster overlay with KCL

The released KCL module includes
`examples/gitops-bootstrap-values.k`. Change only the reviewed CIDR:

```python
values = {
    controller.gateway.overlayCIDR = "100.96.0.0/16"
}
```

Render the standard Helm values overlay:

```sh
kcl run examples/gitops-bootstrap-values.k -S values >cluster-values.yaml
```

Use the result with plain Helm or as input to the user's existing Flux or Argo
CD configuration. KCL remains optional and produces ordinary YAML.

## How ordering remains fail closed

The chart uses standard Kubernetes and Helm lifecycle mechanisms:

1. A Helm pre-install Job creates or validates the namespaced observation CA
   and serving Secret using a temporary, namespaced ServiceAccount and Role.
2. The controller starts with that identity.
3. A non-privileged CNI-installer init container waits for the controller's
   readiness endpoint.
4. Only then does the privileged installer atomically add the chained CNI
   plugin and its release-bound receipt.
5. Node agents start only when the exact CNI files and receipt exist, then
   publish current authenticated capability.

An enrolled workload still cannot schedule or obtain a working sandbox until
the exact route and observed data plane are ready. The bootstrap does not grant
application containers capabilities or place credentials in workloads.

## Current lifecycle boundary

This slice simplifies a **clean install**. Upgrade, rollback, certificate
rotation, repair, and destructive uninstall still use their existing reviewed
`waycloakctl` transactions until equivalent GitOps-driven transitions pass the
same interruption and packet-path acceptance tests. A direct version change is
rejected safely by the immutable gateway class rather than partially activated.
