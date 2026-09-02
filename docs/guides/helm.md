# Helm and OCI guide

Helm is Waycloak's primary installation package. For stable `v1.0.1`, the chart
is published at `oci://ghcr.io/amoenus/charts/waycloak:1.0.1` with digest:

```text
sha256:c4d46e1908625a6e3494db3dd99b2e8c4c47b947a8a8c2ee40a541b2e9af707b
```

The digest in a verified `release-manifest.json` is authoritative. Never use a
mutable `latest` reference.

## Pull and inspect the chart

```sh
helm pull oci://ghcr.io/amoenus/charts/waycloak --version 1.0.1
helm show chart oci://ghcr.io/amoenus/charts/waycloak --version 1.0.1
helm show values oci://ghcr.io/amoenus/charts/waycloak --version 1.0.1
```

Compare the pulled artifact digest with the verified release manifest or
`waycloak-chart.ref`. The chart `appVersion` must be `v1.0.1`.

For a repository checkout, validate development templates with:

```sh
helm lint charts/waycloak
helm template waycloak charts/waycloak --namespace waycloak-system
```

Development defaults intentionally do not invent a releasable identity or
runtime image digests.

## Supported installation flow

The chart alone cannot safely establish the host CNI chain, exact runtime
identity, observation certificates, immutable gateway class, or activation
ordering. Generate those values from the signed release and the observed
cluster:

```sh
waycloakctl install plan \
  --context my-cluster \
  --release-manifest release-manifest.json \
  --overlay-cidr 100.96.0.0/16 \
  --namespace waycloak-system \
  --node-architecture amd64 \
  --output json >install-plan.json

jq -r .valuesYAML install-plan.json >reviewed-values.yaml
helm template waycloak \
  oci://ghcr.io/amoenus/charts/waycloak \
  --version 1.0.1 \
  --namespace waycloak-system \
  --values reviewed-values.yaml >rendered-waycloak.yaml
```

Rendering is useful for policy review and GitOps diffing. Installation still
uses the exact confirmation-bound plan:

```sh
PLAN_ID="$(jq -r .planID install-plan.json)"
waycloakctl install apply \
  --context my-cluster \
  --plan install-plan.json \
  --confirm "$PLAN_ID"
```

Do not convert this into a raw `helm install` command. The CLI performs
preconditions and postconditions that Helm cannot represent.

## Values operators may intentionally expose

After the exact release transaction, GitOps may manage non-lifecycle values
that do not change release identity or the fail-closed boundary. The common
example is observability:

```yaml
observability:
  metrics:
    enabled: true
  assets:
    enabled: true
  openTelemetry:
    otlpEndpoint: https://otel-collector.observability.svc:4318
```

The generated chart schema is the field-level authority. Do not override
release versions, manifest digests, image repositories/digests, CNI paths,
node-agent hold state, observation trust, or default gateway-class identity.

## GitOps handoff

Use this boundary with Argo CD, Flux, or another reconciler:

1. Commit the verified target chart digest and reviewed plan-derived values.
2. Suspend automatic runtime synchronization for a changed release.
3. Run `waycloakctl install plan/apply` against that exact target.
4. Run `waycloakctl doctor` and verify the target release is healthy.
5. Resume GitOps so it verifies or converges the already-matching release.

The immutable default gateway class intentionally prevents a raw GitOps release
change from silently advancing executable components. GitOps is the desired
state recorder, not release-transition authority.

## Upgrade and rollback

Forward upgrades and rollbacks use the same verified plan/apply process. A
Helm revision number is not sufficient evidence of the target's chart, CRDs,
images, class, CNI receipt, or certificates. If a transaction leaves a pending
or corrupt Helm state, use `waycloakctl install repair plan` and its reviewed
apply flow; do not guess with `helm rollback`.

Normal Helm uninstall does not remove CRDs and does not restore the host CNI
chain. Use the documented confirmation-gated lifecycle in
[Turnkey bootstrap](../implementation/turnkey-bootstrap.md).

## Related configuration

- [Getting started](../getting-started.md)
- [Configuration reference](../configuration.md)
- [Advanced setup](../advanced-setup.md)
- [Observability](../operations/observability.md)
