# Waycloak Helm chart

First-time users should follow the repository's
[getting-started guide](../../docs/getting-started.md). It verifies the signed
release manifest, pulls this chart by its recorded OCI digest, prepares webhook
TLS, creates a gateway, and protects a disposable workload.

This chart installs the Waycloak CRDs, controller/webhook Deployment, Service, least-privilege RBAC, admission configurations, and controller disruption budget.

All three image digests and webhook TLS are mandatory. By default the chart
consumes an externally managed TLS Secret and CA bundle. Clusters that already
run cert-manager may opt into a chart-owned self-signed serving certificate and
CA injection; cert-manager is never a Waycloak runtime dependency. The chart
never emits mutable image references. See the repository
[installation guide](../../docs/operations/install.md) for both certificate
modes, required security exceptions, and gateway creation.

```sh
helm lint charts/waycloak \
  --set images.controller.digest="sha256:$CONTROLLER_DIGEST" \
  --set images.agent.digest="sha256:$AGENT_DIGEST" \
  --set images.gatewayManager.digest="sha256:$GATEWAY_MANAGER_DIGEST" \
  --set webhook.tls.existingSecret=waycloak-webhook-tls \
  --set-string webhook.tls.caBundle="$CA_BUNDLE"
```

For a declarative cert-manager installation, replace the two webhook TLS flags
with `--set webhook.tls.certManager.enabled=true`.

CRDs follow Helm's `crds/` lifecycle: they install before namespaced resources and are not deleted during uninstall.
