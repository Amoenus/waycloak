# Waycloak

Waycloak is a clean-break, Kubernetes-native private-egress system. An explicitly enrolled Pod is admitted only through the stable `VPNEgressRoute` API and a same-namespace Pod-template label:

```yaml
spec:
  template:
    metadata:
      labels:
        networking.waycloak.io/egress-route: private-egress
```

The baseline security boundary is a mandatory chained CNI plugin backed by a privileged per-node agent. It installs deny-first state before CNI `ADD` succeeds. Waycloak injects no application sidecar or init container, Linux capability, host mount, VPN credential, or Kubernetes credential. A missing, rejected, unauthorized, unhealthy, or unprogrammed route fails closed; it never silently selects ordinary egress.

## Current state

`v0.1.0-rc.2` is the current feature-complete release candidate. The public
`networking.waycloak.io/v1beta1` schemas and behavioral contracts are frozen;
the alpha runtime is absent. The release provides signed CLI binaries,
multi-platform OCI images, a Helm OCI chart, an optional KCL OCI module, SBOMs,
provenance, and an exact signed release manifest. It is a prerelease and does
not claim final stable graduation.

Start with [Getting started](docs/getting-started.md), then read the [use
cases](docs/use-cases.md), [configuration requirements](docs/configuration.md),
[deployable resources](docs/deployable-resources.md), and [API
reference](docs/api/v1beta1.md).

The old [PRD](docs/product/PRD.md) and [alpha API contract](docs/api/api-contract.md) are as-built evidence only. They are not compatibility inputs.

## License

Waycloak is licensed under the [MIT License](LICENSE). Apache-2.0 material retains its required notices.
