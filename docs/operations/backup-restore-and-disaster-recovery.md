# Backup, restore, and disaster recovery

Status: portable exact-release slice implemented; full support-row disaster
recovery certification remains open in issue #32

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
   classes. Verify its CRDs and Core runtime.
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

6. Confirm that gateways, routes, and any Extended leases acquire new UIDs and
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

## Failure and degraded-state handling

- Missing route, gateway, class, Secret, ConfigMap, CNI, node agent, tunnel, or
  DNS remains unavailable and fail closed.
- A logical restore never imports `Ready=True`; readiness must be re-observed.
- A provider mapping that cannot be reacquired leaves the Extended lease not
  Ready and must not cross-deliver an old mapping.
- Keep workloads stopped if target support, exact artifacts, or observation is
  uncertain. Do not use ordinary egress as a recovery backend.
- Normal Helm uninstall leaves CRDs and intent. Destructive CRD purge requires
  the separate confirmation-gated procedure.

The declared portable objective is RPO at the last retained state export and
RTO within 30 minutes after the compatible cluster, exact release, namespaces,
ConfigMaps, and Secrets are available. This target is not stable-certified until
the complete support matrix and real-provider disaster drills pass.
