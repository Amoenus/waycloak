# Waycloak Helm chart

This chart installs the Waycloak CRDs, controller/webhook Deployment, Service, least-privilege RBAC, admission configurations, and controller disruption budget.

All three image digests and externally managed webhook TLS are mandatory. The chart never emits mutable image references or random certificate material. See the repository [installation guide](../../docs/operations/install.md) for certificate preparation, required security exceptions, and gateway creation.

```sh
helm lint charts/waycloak \
  --set images.controller.digest="sha256:$CONTROLLER_DIGEST" \
  --set images.agent.digest="sha256:$AGENT_DIGEST" \
  --set images.gatewayManager.digest="sha256:$GATEWAY_MANAGER_DIGEST" \
  --set webhook.tls.existingSecret=waycloak-webhook-tls \
  --set-string webhook.tls.caBundle="$CA_BUNDLE"
```

CRDs follow Helm's `crds/` lifecycle: they install before namespaced resources and are not deleted during uninstall.
