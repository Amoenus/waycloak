# Backup, restore, and disaster recovery

Status: certified for portable exact-release recovery and the declared hosted
K3s single-server embedded-etcd row. Other datastore topologies require their
own support rows.

Waycloak never relaxes fail-closed egress to accelerate recovery. Keep enrolled
workloads stopped until newly restored intent has acquired new runtime identity
and current live conditions.

## Choose the recovery boundary

Use a Kubernetes-distribution datastore snapshot only when the entire cluster,
including encrypted Secret state and Kubernetes object UIDs, is restored as one
supported point in time. Follow the distribution's snapshot procedure. Do not
mix selected objects from a snapshot with portable restore and do not claim UID
preservation from YAML.

Use portable state for a fresh exact-release installation. It preserves
operator/workload intent but deliberately reacquires every runtime identity.

## Supported K3s embedded-etcd snapshot row

The first distribution-native row is deliberately narrow:

- K3s `v1.36.1+k3s1`, installed from its exact SHA-256-verified binary;
- one server using embedded etcd through `--cluster-init`;
- the bundled containerd and Flannel data plane; and
- one local K3s etcd snapshot retained together with the exact server token.

This row does not cover SQLite, an external datastore, multiple servers, S3
snapshot transport, or a different Kubernetes distribution. Those are separate
support rows and must not inherit this evidence.

Certification does not require repeating a destructive datastore restore on a
healthy application cluster or provisioning a second cluster node. The hosted
row exercises the exact distribution binary, CNI/runtime boundary, coherent
Kubernetes identities, cold restore procedure, and packet assertions. An
operator may run the same procedure on a disposable target for environment
qualification, but that is deployment-specific evidence rather than a hidden
Waycloak release prerequisite.

Use the distribution-native `k3s etcd-snapshot save` command. Retain the
snapshot and server token in a root-only encrypted backup boundary. A K3s
snapshot contains the complete datastore, including Kubernetes Secrets and
cluster certificate authority private keys. The same server token is required
to decrypt confidential bootstrap data during restore. Never upload either CI
fixture artifact or print the token.

This row supports only a cold restore. A service-only stop is insufficient:
K3s can leave containerd workloads running, and the hosted proof observed an
enrolled application continue with direct egress during such a warm reset.
Before stopping K3s, enumerate every exact sandbox through the K3s containerd
CRI endpoint, stop and remove each one, and require both the CRI container and
sandbox inventories to be empty. If CRI cannot be queried or quiesced, abort.

At snapshot time, retain a root-only digest set for Waycloak's exact CNI binary,
active chained conflist, and durable attachment files, plus an exact copy of the
active chained conflist. After CRI quiescence, stop K3s, verify that digest set,
and run `k3s server --cluster-reset` with the exact
`--cluster-reset-restore-path` and retained token. Before starting the ordinary
server service, restore the saved chain both at its distribution path and as an
independent Waycloak-owned `00-waycloak.conflist`; verify it is the
lexicographically first CNI configuration. K3s may regenerate its Flannel file
during startup, so relying only on that distribution-owned path is unsupported.
The reset command is one-shot; do not leave `--cluster-reset` on the service.
K3s moves the replaced datastore aside under `etcd-old-*`. Keep that directory
inside the confidential boundary until the restored cluster and an independently
retained snapshot are verified, then remove it through separately reviewed
exact-path cleanup.

RPO is the exact completed snapshot point. RTO is measured from server stop
through API readiness and Node `Ready`; the hosted gate records both snapshot
and restore seconds in its workflow summary. Application recovery can take
longer because a restored live-looking observation is immediately treated as
stale until the node agent and protected path are freshly observed.

Before reporting the row recovered, require all of the following:

1. Namespace, Pod, and `VPNWorkloadBinding` UIDs return together and a
   post-snapshot marker does not.
2. The binding controller changes restored stale `Ready=True` and
   `NodeReady=True` to current-generation `False` conditions.
3. CRI was empty before reset; the exact Waycloak CNI binary, first chained
   configuration, attachment intent, and deny state survive recovery; `CHECK`
   remains denied; and no enrolled application container starts after failed
   `ADD`. A recreated sandbox may have a new CRI identity, but the exact
   Kubernetes Pod UID and attachment authority must remain coherent.
4. TCP, UDP, DNS over UDP and TCP, and fragmented-UDP direct packet counters do
   not change through restore.
5. Idempotent `DEL`/`GC` succeeds and a second unenrolled positive control proves
   the primary CNI is functional.

Any identity drift, surviving post-snapshot state, missing protected deny state,
unavailable observation, or unexplained packet makes the cluster degraded. Keep
enrolled workloads stopped and do not substitute ordinary egress.

## Create and retain portable state

Run the read-only command after each reviewed intent change and store the result
in an encrypted backup system outside the cluster:

```text
waycloakctl state backup --context <source-context> --output json \
  >waycloak-state.json
```

Review `excluded` in the output. Independently back up:

- provider credential Secrets at their source of truth;
- native gateway ConfigMaps;
- namespace declarations and labels used for attachment consent;
- workload Deployments/StatefulSets and their Pod-template enrollment labels;
- application data; and
- the verified release manifest, signatures, SBOM, and provenance evidence.

Do not add Secret data, status, bindings, allocations, public addresses, or
provider mappings to the state file.

## Portable restore

1. Install the exact signed Waycloak release recorded by the required gateway
   classes. Verify its CRDs and baseline runtime.
2. Restore namespaces, namespace consent labels, native ConfigMaps, and
   credential Secrets from their separate sources. Do not start enrolled
   workloads.
3. Produce a target-bound plan:

   ```text
   waycloakctl state restore plan --context <target-context> \
     --backup waycloak-state.json --overlay-cidr <reviewed-cidr> \
     --output json >restore-plan.json
   ```

4. Review the target cluster fingerprints, observation digest, exact CRD and
   class identities, namespaces, resources, warnings, and `planID`.
5. Apply only the reviewed plan:

   ```text
   waycloakctl state restore apply --context <target-context> \
     --plan restore-plan.json --confirm <exact-planID>
   ```

6. Confirm that gateways, routes, and any optional port-forward leases acquire new UIDs and
   current-generation conditions. Confirm no `VPNWorkloadBinding` existed before
   workload restart.
7. Restart workloads in controlled batches. For every enrolled Pod, require a
   new exact Pod-UID binding and live `Ready=True`. Run protected/unprotected,
   DNS, tunnel-loss, and direct-packet verification.
8. For port forwarding, wait for a new provider mapping, exact SingleActive Pod
   UID, gateway rule observation, delivery, and application acknowledgement.
   Never assume the previous public address or port was retained.

Apply refuses a changed target observation, changed CRD/class identity,
missing namespace, tampered plan, or pre-existing unowned object before its
first mutation. Repeating an exact partially applied plan is safe; changing the
backup or target requires a new plan and confirmation.

## Exact release transition and rollback

Do not use `helm rollback` as the release-identity boundary. Retain the signed
manifest and verification evidence for every supported target, including the
prior target used for rollback.

1. Keep the current deny path installed and stop rollout of newly enrolled
   workloads.
2. Verify the intended target release manifest, signatures, SBOM, provenance,
   platforms, and support row independently of the cluster.
3. Run `waycloakctl install plan` with that exact manifest. Review the observed
   source Helm revision, release and image identities, CRD specification
   identities, gateway-class UID/generation, observation-certificate public
   identities, and target.
4. Apply only that plan ID. Apply re-observes the source and target chart before
   mutation and refuses drift. The initial beta lifecycle refuses every CRD
   schema/storage change; those require a dedicated storage-migration plan.
5. Require a newer Helm revision, the exact target release/runtime/CNI receipt,
   a new immutable gateway-class UID at the stable class name, preserved
   observation-certificate identity, healthy node capability and gateway
   activation, and protected packet/startup checks before resuming workloads.

Use the same procedure for forward transition and rollback. A missing or
tampered observation identity is not repaired by silently rotating trust. If
only the public CA Secret is missing, apply may reconstruct it from the intact,
release-owned TLS Secret; if the serving identity is missing, stop and use the
explicit certificate-rotation recovery procedure.

A changed deployed release uses two Helm revisions while the deny path remains
installed. The first applies the target controller, CNI installer, and class but
pins the node agent to the exact reviewed source image and release identity. The
second activates the target node agent only after the target CNI receipt is
Ready. Do not collapse these revisions: simultaneous CNI-installer and agent
replacement can strand both behind a missing local agent socket. This staging
is not a bypass or fallback; protected sandboxes continue to fail closed.

### Resume an interrupted exact transition

Retain the reviewed plan until completion. Before class withdrawal,
`waycloakctl` writes the immutable
`<release>-release-transition` ConfigMap in the system namespace. It contains
the non-sensitive reviewed plan and exact plan ID, not credentials. Do not edit,
replace, or delete it.

1. Stop new enrolled rollout and leave the CNI, prior node agent, and deny state
   installed.
2. Re-run `waycloakctl install plan` with the same verified target manifest.
   At an exact checkpoint it returns the original journaled plan ID. A different
   target or changed preflight is refused.
3. Review that recovered plan and run `install apply` with its exact original
   confirmation. Apply skips an already completed class withdrawal or staging
   revision and activates only the missing exact stage.
4. Require the journal to be absent, the exact target postconditions and doctor
   health to pass, then activate singleton gateways and resume workload rollout.

Recovery is supported only when the live state is the exact source with its
class withdrawn, the exact target stage retaining the source agent, or the
completed exact target. A missing/foreign journal, pending or ambiguous Helm
revision, arbitrary image skew, or changed trust/CRD/cluster identity is a hard
stop. Capture a support bundle and author a separate repair plan; do not delete
Helm locks, recreate trust, invoke opaque `helm rollback`, or permit ordinary
egress to make progress.

## Rotate observation trust

Do not replace `<release>-observation-ca` or
`<release>-observation-tls` manually. Retain the current deny path and run:

```text
waycloakctl certificate rotation plan \
  --namespace waycloak-system --release waycloak --output json \
  > certificate-rotation-plan.json
waycloakctl certificate rotation apply \
  --plan certificate-rotation-plan.json \
  --confirm <exact-planID>
```

Review the source release, preflight digest, stable Secret UIDs, public
certificate digests, and rotation sequence. The plan and immutable journal are
non-sensitive; the newly generated private key exists only in the staged and
stable TLS Secrets. Wrong confirmation performs no mutation.

During overlap and new-only verification, node agents retain the local CNI and
deny state but publish a capability hold. New enrolled scheduling therefore
remains unavailable while fresh authenticated observation is proved through
each trust boundary. Existing protected traffic remains subject to the same
gateway/tunnel deny path; there is no plaintext listener or ordinary-egress
fallback.

If apply is interrupted, leave the stable Secrets, staged Secret, journal, and
DaemonSet unchanged. Re-run `certificate rotation plan` with the same namespace
and release; an exact recoverable checkpoint returns the original plan. Apply
that plan with its original confirmation. Foreign/tampered material, changed
release/preflight/Secret UID, an unenumerated bundle, or missing staged private
material before the exact target is refused. Successful completion preserves
the stable Secret UIDs, leaves one new CA and serving identity, removes the
capability hold, restores fresh CNI-ready capability, and deletes staged state
and the journal.

## Failure and degraded-state handling

- Missing route, gateway, class, Secret, ConfigMap, CNI, node agent, tunnel, or
  DNS remains unavailable and fail closed.
- A logical restore never imports `Ready=True`; readiness must be re-observed.
- A provider mapping that cannot be reacquired leaves the port-forward lease not
  Ready and must not cross-deliver an old mapping.
- Keep workloads stopped if target support, exact artifacts, or observation is
  uncertain. Do not use ordinary egress as a recovery backend.
- Normal Helm uninstall leaves CRDs and intent. Destructive CRD purge requires
  the separate confirmation-gated procedure.

The declared portable objective is RPO at the last retained state export and
RTO within 30 minutes after the compatible cluster, exact release, namespaces,
ConfigMaps, and Secrets are available. This target is not stable-certified until
the complete support matrix and real-provider disaster drills pass.
