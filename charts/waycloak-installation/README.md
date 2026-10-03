# Declarative installation

This chart installs a separate controller and the cluster-scoped
`WaycloakInstallation/waycloak` resource. The controller verifies the requested
release and reconciles the runtime chart in place. `waycloakctl` and the Helm
executable are not used by that controller.

The signed `v1.0.2-rc.4` candidate is published. Declarative adoption from rc.3,
configuration changes, interrupted-controller recovery, and rollback/return have
passed live qualification. See [project status](../../PROJECT_STATUS.md) for
the evidence and remaining legacy-adoption and soak limits.

## Namespace and installation

The runtime requires privileged node components. Create its dedicated namespace
with an explicit Pod Security policy before installing the controller:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: waycloak-system
  labels:
    pod-security.kubernetes.io/enforce: privileged
```

Install the published chart into that namespace:

```sh
helm upgrade --install waycloak-installation \
  oci://ghcr.io/amoenus/charts/waycloak-installation \
  --version 1.0.2-rc.4 --namespace waycloak-system --values installation-values.yaml
```

The release package supplies the exact signed controller image digest. Its
default desired runtime version is the chart's application version. Keep
`installation-values.yaml` under version control; for a mixed-architecture
cluster, select the applicable support row explicitly:

```yaml
nodeArchitecture: amd64
config:
  controllerResources:
    requests:
      cpu: 25m
      memory: 96Mi
    limits:
      memory: 256Mi
```

Changing the chart version or supported configuration is sufficient to request
a transition. Argo CD and other GitOps tools can manage this chart normally.
Do not let another manager also reconcile the runtime release's resources.

For plain Kubernetes, apply the release's `waycloak-installation.yaml` bundle
after the namespace. Subsequent applies change installation desired state:

```yaml
apiVersion: installation.waycloak.io/v1alpha1
kind: WaycloakInstallation
metadata:
  name: waycloak
spec:
  version: v1.0.2-rc.4
  namespace: waycloak-system
  release: waycloak
  overlayCIDR: 100.96.0.0/16
  nodeArchitecture: amd64
  config: {}
```

KCL can emit this same object; no additional runtime integration is required.
Gateway and route resources remain the networking API. Provider credentials
remain referenced by gateways and are never copied into this installation object.

## Existing runtime adoption

Stop the previous runtime manager from reconciling and pruning resources, retain
the existing Helm release name and namespace, then set `adoptExisting: true` in
the installation chart values (or `spec.adoptExisting` in plain YAML).
Choose a successor release with declarative lifecycle support. Preserve any
port-forward configuration by explicitly supplying its existing trust reference:

```yaml
adoptExisting: true
config:
  portForwarding:
    controllerTLSSecret: existing-controller-tls
    adapterEnabled: true
```

The referenced Secret must already be immutable and have the correct controller
mTLS identity. This transition does not rotate application or provider Secrets.
The owner record binds subsequent operations to this installation's UID. Keep
the installation object during upgrades; deleting and recreating it does not
authorize adoption of another UID's journal.

Legacy Flannel layouts are migrated under the deny hold to a separately owned
`05-waycloak.conflist`. The preserved original must reproduce the existing chain.
The installer writes and syncs the owned guarded chain before restoring the
upstream primary, so a restart can regenerate the primary without removing
Waycloak's selected chain. Existing owned layouts retain their paths.

## Progress, interruptions, and rollback

```sh
kubectl get waycloakinstallation waycloak -w
kubectl get waycloakinstallation waycloak -o yaml
```

`Ready=True` with `status.readyGeneration` equal to `metadata.generation` reports
verification of the requested runtime, node coverage, trust, gateways, and live
workload bindings. Deployment readiness alone does not establish this condition.

An immutable journal records the active generation. A controller restart resumes
that target; a newer version/configuration waits until it completes. Setting
`suspend: true` stops new transitions and allows the active transition to finish.
Failures retain the journal and denial state. Inspect the Ready condition and
controller logs; do not delete Helm revisions or the journal to force progress.

Rollback uses the same version field and guarded sequence. Releases before
`v1.0.2-rc.3` are rejected as targets because of their terminating-namespace
fail-open defect. Networking API/storage changes unsupported by the current
transition are rejected before executable mutation. Configuration-only changes
require the declarative lifecycle release; rc.3 remains a version-transition
fallback subject to its older chart schema.

The installation controller runs with host networking so broken chained CNI
cannot prevent it from starting. It needs outbound HTTPS access to the published
GitHub release, Sigstore trust services, and the public OCI registry. It has a
separate leader Lease and resource limits. Port `18081` is reserved on its host
for health probes and can be changed through `healthPort`.

Uninstalling this chart retains the installation object and runtime. Runtime
removal is a separate decommissioning operation; it must preserve the fail-closed
invariant for any remaining protected workloads.
