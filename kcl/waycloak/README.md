# Waycloak KCL package

This optional package contains generated KCL schemas for the clean-break
`networking.waycloak.io/v1beta1` API. The CRDs under `config/crd/bases` are the
single source of truth; `hack/generate-kcl-models.sh` reproduces these models.

The package contains schemas for `VPNGatewayClass`, `VPNGateway`,
`VPNEgressRoute`, `VPNWorkloadBinding`, `PortForwardLease`, and
`WorkloadAdapter`. `VPNWorkloadBinding` is controller-authored and is included
for validation and tooling, not as a user manifest surface.

The release publishes this package both as a downloadable archive and as the
signed OCI module `oci://ghcr.io/amoenus/waycloak-kcl`. Select the same immutable
version as the Waycloak release and verify its digest through
`release-manifest.json` or `waycloak-kcl.ref`:

```sh
kcl mod add oci://ghcr.io/amoenus/waycloak-kcl --tag 1.0.1
kcl run examples/private-egress.k -S items
```

The stable module digest is
`sha256:4ccad6c38eac0028fe9aa7466b1cf7bce664f1cc25db5365288245361ebac45b`.
See [`docs/guides/kcl.md`](../../docs/guides/kcl.md) for dependency imports,
rendering, GitOps layout, and credential boundaries.

Use the examples as authoring references after the complete Helm/CLI
installation is healthy. This package does not install a controller or data
plane. Pod enrollment is exactly one same-namespace route label:

```yaml
networking.waycloak.io/egress-route: private
```

There are no compatibility helpers for legacy annotations or objects.
