# Upgrade and rollback

Waycloak controller/webhook replicas roll with `maxUnavailable: 0` and may
surge by 100 percent. A generation change makes every old replica unready at
once, so the full surge capacity is required to start a complete matching
replica set without deadlocking the Deployment rollout. Gateway StatefulSets
are deliberate singletons and use `OnDelete`: reconciling a new engine,
manager image, or Pod template does not restart a working tunnel automatically.

## Verify release evidence

Download the chart package, release manifest, and Sigstore bundle from the GitHub release. Verify the manifest and every OCI digest as described in [the release guide](../release/releasing.md). Use the local, attested chart package for the upgrade rather than a mutable registry tag.

## Upgrade the control plane

Record the current revision and values, then upgrade:

```sh
helm history waycloak -n waycloak-system
helm get values waycloak -n waycloak-system --all >waycloak-values-before-upgrade.yaml
helm upgrade waycloak ./waycloak-NEW_VERSION.tgz \
  --namespace waycloak-system \
  --reuse-values \
  --wait --timeout 5m
```

The source chart's released defaults contain immutable image digests. Explicit
site overrides must also remain digest-pinned. The chart hashes the immutable
controller and agent identities into a desired admission generation stored in
`<release>-admission-generation`. Every replica compares its local generation
with that ConfigMap through an uncached API read for readiness and again for
each opted-in admission request. Confirm both controller replicas are Ready,
their `admission.networking.waycloak.io/generation` Pod annotation matches the
ConfigMap, and admission still rejects an annotated missing-gateway reference
while leaving an unannotated Pod unchanged.

### From v0.3.1 to v0.3.2

`v0.3.2` changes the controller and gateway-manager only. The agent and
workload-adapter contracts are unchanged. Upgrade the control plane first and
confirm both replicas are Ready. The gateway StatefulSet then records the new
manager digest but remains `OnDelete`; activate that manager during a separate
maintenance window by deleting only the serving singleton Pod.

During the post-upgrade soak, collect the structured
`gluetun_transport_verification`, `gateway_health_transition`, and
`gateway_config_source_transition` events. A recovered one-off loopback
transport error stays visible without withdrawing readiness; an HTTP health
failure, timeout, or repeated transport error still withdraws gateway and
lease readiness fail closed.

### From v0.2.2 to the v0.3.0-alpha.1 certification candidate

Install only the chart package and digest identities from the signed
`v0.3.0-alpha.1` release manifest. The candidate adds the cluster-scoped
`WorkloadAdapter` trust API and the native `VPNGateway.spec.engine.config`
surface while retaining the mutually exclusive legacy `spec.provider` shape.
Apply the CRDs through the chart before creating either new object shape.

Upgrade the control plane first and verify its admission generation before
rolling protected workloads. Existing legacy Proton/OpenVPN gateways may
remain unchanged during this candidate upgrade. Migrating one to native
configuration changes the gateway Pod template but does not restart the
singleton automatically; validate the ConfigMap and engine-only Secret
references, then perform the documented maintenance-window Pod deletion.
Protected workloads remain fail closed throughout that restart.

This prerelease is a certification input, not proof that the v0.3 compatibility
gate is complete. Do not promote it to production consumers or retire the PoC
until the signed real-provider qBitTorrent run and Bitmagnet/Loadstone adoption
have produced their required evidence.

## Roll protected workloads after the control plane

The generation gate makes an agent-changing rollout fail closed. After Helm
updates the desired generation, an old replica immediately becomes unready and
rejects any opted-in request that reaches it with
`AdmissionGenerationConflict`; a new replica does the same until its local
generation is selected. The webhook match condition still excludes
unannotated Pods, so their admission does not depend on this gate.

Keep the two-phase sequence as the low-disruption procedure: wait for the
control plane before deliberately rolling protected workloads. Concurrent
GitOps application is safe from mixed injection, but an opted-in Pod creation
may be rejected and retried while replicas transition.

After the control plane converges, roll protected workloads and confirm each
new Pod's `waycloak-prepare`, `waycloak-verify`, and `waycloak-agent` image
references match the intended release manifest. Its
`internal.networking.waycloak.io/admission-generation` annotation must match
the selected ConfigMap generation. Unannotated Pods remain outside this
sequencing requirement.

## Activate gateway changes

When the gateway template changes, the controller emits `GatewayRolloutRequired`. Schedule an outage for each gateway separately:

```sh
kubectl get events -n waycloak-egress --sort-by=.lastTimestamp
kubectl delete pod -n waycloak-egress waycloak-gateway-GATEWAY_HASH-0
kubectl wait --for=condition=Ready pod -n waycloak-egress waycloak-gateway-GATEWAY_HASH-0 --timeout=5m
kubectl wait --for=condition=Ready vpngateway/example -n waycloak-egress --timeout=5m
```

Protected workloads remain fail closed while the singleton restarts. Do not delete multiple gateways simultaneously unless their combined outage is intentional. The PDB blocks eviction-based drains but does not block an explicit Pod deletion.

## Roll back

If control-plane validation fails, roll back to the recorded Helm revision:

```sh
helm rollback waycloak PREVIOUS_REVISION -n waycloak-system --wait --timeout 5m
```

Rollback restores the previous digest values and controller template stored by Helm. Gateway Pods still use `OnDelete`; if a gateway was already restarted onto the new manager or engine, restore the previous `VPNGateway` specification and then delete that gateway Pod during another maintenance window.

Rollback below v0.3.0 is supported only for the documented Proton/OpenVPN
native shape whose provider, protocol, single country selector, and
`username`/`password` Secret map losslessly to the mutually exclusive legacy
`spec.provider` fields. Convert and verify those gateways before downgrading.
Mullvad/WireGuard, custom WireGuard or OpenVPN, and configurations using other
native-only Gluetun settings have no lossless legacy representation and must
remain on v0.3.0 or newer. Pre-v0.3 controllers do not understand native engine
references. Do not rely on CRD rollback: Helm does not safely downgrade stored
custom resources, and an old controller must remain fail closed rather than
infer provider settings.

## Rotate webhook TLS

Rotate trust without an admission outage:

1. upgrade the release with a CA bundle containing both old and new CA certificates;
2. replace `tls.crt` and `tls.key` in the externally managed Secret;
3. force a zero-unavailable controller rollout with a changed Pod annotation;
4. prove annotated and unannotated admission behavior;
5. upgrade again with only the new CA bundle.

Never replace the serving certificate before the API server trusts its issuer. The gated Helm lifecycle acceptance executes this two-phase sequence and then exercises fail-closed admission.
