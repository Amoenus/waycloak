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
