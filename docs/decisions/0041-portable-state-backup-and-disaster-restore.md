# ADR 0041: Portable intent backup reacquires runtime identity

Status: Accepted implementation boundary for issue #32
Date: 2026-08-09

## Context

Waycloak has two materially different recovery mechanisms. A Kubernetes
datastore snapshot can preserve Kubernetes UIDs only when the distribution
restores one coherent cluster-wide point in time. A portable logical backup
must work on a newly installed cluster, whose API server assigns new UIDs and
whose provider, gateway, node, Pod, and network observations cannot truthfully
be imported.

Treating controller status as desired state would be dangerous. In particular,
an old `VPNWorkloadBinding`, allocation, gateway UID, provider mapping, applied
generation, or `Ready=True` observation cannot authorize packets in the target
cluster. Secret material also has a separate source of truth and must not be
copied into an ordinary Waycloak state artifact.

## Decision

Waycloak supports two explicitly separate boundaries:

1. A distribution-native datastore snapshot is an all-cluster recovery. Its
   distribution owns snapshot consistency, encryption, Secret handling, UID
   preservation, and etcd recovery. Waycloak tests each published support row
   before claiming this path.
2. `waycloakctl state backup` creates a portable logical backup. It contains
   only the `spec` and names of namespaced `VPNGateway`, `VPNEgressRoute`,
   `PortForwardLease`, and `WorkloadAdapter` objects. The format structurally
   has no arbitrary metadata or status field.

The portable artifact records a canonical digest, hashed source-cluster
identity, exact replacement CRD spec digests, and the complete immutable
identity of every referenced `VPNGatewayClass`. It is deterministic for
unchanged state. It refuses an object that is being deleted.

The portable artifact never contains:

- Secret or ConfigMap contents;
- Pods, Deployments, StatefulSets, or workload enrollment manifests;
- `VPNWorkloadBinding` objects or allocation addresses;
- UIDs, resource versions, owner references, finalizers, conditions, status,
  applied generations, node observations, addresses, or endpoints; or
- gateway runtime state, provider mappings, live lease state, or credentials.

An operator restores credential Secrets, native ConfigMaps, namespaces, and
workload controllers through their own reviewed source of truth. The exact
signed Waycloak release is installed first and must provide matching CRDs and
gateway classes.

`waycloakctl state restore plan` binds the backup to a current target preflight
observation, overlay, CRD identities, class identities, namespaces, and every
portable object. Apply requires the exact plan digest, repeats all read-only
checks, refuses all unowned name conflicts before the first write, and creates
objects atomically with the fixed `waycloakctl-state-restore` field manager. It
never patches or adopts a pre-existing object. An exact partial retry is
idempotent.

Restore creates new Kubernetes objects and therefore new UIDs. Controllers
must reacquire gateway instances, bindings, allocations, provider mappings,
lease handoffs, and all live observations. Protected workloads remain stopped
until the restored route and gateway plus their newly acquired data plane
report current-generation `Ready=True`. A missing or unhealthy dependency is a
visible availability failure and never permits ordinary egress.

Portable restore initially requires the exact CRD schema identity recorded by
the backup. Cross-version restore requires the separately tested beta
storage-version and upgrade procedure; the restore tool does not perform API
conversion or call `helm upgrade` as a CRD migration substitute.

## Recovery objectives

- Portable-backup RPO is the last successful, externally retained backup after
  an intent change. Waycloak does not claim continuous backup.
- The stable target for the exact-release portable restore is 30 minutes after
  a compatible cluster, release, namespaces, ConfigMaps, and Secrets are
  available. Provider allocation and port availability can extend application
  recovery and are reported through conditions rather than hidden.
- Distribution-snapshot RPO and RTO are support-row properties and are not
  inherited from the portable target.
- The fail-closed packet invariant has no recovery-time exception and applies
  before, during, and after both recovery mechanisms.

## Consequences

- Portable recovery cannot promise the same VPN address, public port, gateway
  UID, lease UID, or Pod UID.
- `PortForwardLease` intent can be restored, but the provider mapping is new and
  the application must acknowledge the new exact generation.
- Exact UID preservation requires a coherent datastore restore, never a YAML
  export or object recreation.
- Normal uninstall, portable backup, and destructive CRD purge remain separate
  operations.
- Certificate material is regenerated or restored through the installation
  lifecycle, not copied into portable state.

## Alternatives rejected

- Export status and controller-owned bindings: imports stale authority and can
  create identity collision or stale readiness.
- Export Secrets automatically: turns a state inventory into a credential
  exfiltration surface and duplicates the credential source of truth.
- Recreate objects with their old UIDs: Kubernetes does not support this through
  ordinary API creation and pretending otherwise would break exact identity.
- Restore workloads automatically: can start application sandboxes before the
  target data plane is live and crosses workload-owner authority.
- Treat Helm rollback as state recovery: Helm does not back up CR instances,
  credentials, Kubernetes UIDs, or external provider state.

## Related decisions

- [ADR 0031](0031-crd-installation-conversion-and-storage-lifecycle.md)
- [ADR 0032](0032-turnkey-bootstrap-and-preflight.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0037](0037-uid-bound-allocation-and-quarantine.md)
- [ADR 0040](0040-service-backed-single-active-port-forwarding.md)
