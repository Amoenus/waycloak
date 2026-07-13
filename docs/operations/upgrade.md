# Upgrade and rollback

Waycloak controller/webhook replicas roll with `maxUnavailable: 0`. Gateway StatefulSets are deliberate singletons and use `OnDelete`: reconciling a new engine, manager image, or Pod template does not restart a working tunnel automatically.

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

The source chart's released defaults contain immutable image digests. Explicit site overrides must also remain digest-pinned. Confirm both controller replicas are ready and admission still rejects an annotated missing-gateway reference while leaving an unannotated Pod unchanged.

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

## Rotate webhook TLS

Rotate trust without an admission outage:

1. upgrade the release with a CA bundle containing both old and new CA certificates;
2. replace `tls.crt` and `tls.key` in the externally managed Secret;
3. force a zero-unavailable controller rollout with a changed Pod annotation;
4. prove annotated and unannotated admission behavior;
5. upgrade again with only the new CA bundle.

Never replace the serving certificate before the API server trusts its issuer. The gated Helm lifecycle acceptance executes this two-phase sequence and then exercises fail-closed admission.
