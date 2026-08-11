# Waycloak

Waycloak is a clean-break, Kubernetes-native private-egress system. An explicitly enrolled Pod is admitted only through the stable `VPNEgressRoute` API and a same-namespace Pod-template label:

```yaml
spec:
  template:
    metadata:
      labels:
        networking.waycloak.io/egress-route: private-egress
```

The Core security boundary is a mandatory chained CNI plugin backed by a privileged per-node agent. It installs deny-first state before CNI `ADD` succeeds. Waycloak injects no application sidecar or init container, Linux capability, host mount, VPN credential, or Kubernetes credential. A missing, rejected, unauthorized, unhealthy, or unprogrammed route fails closed; it never silently selects ordinary egress.

## Current state

The replacement API, route and enrollment controllers, UID-bound workload bindings, chained CNI, privileged node agent, and gateway class/capability contract are implemented. The alpha runtime has been removed from the replacement source and release surfaces. Turnkey installation, optional port-forward capabilities, lifecycle certification, and stable release evidence remain incomplete; there is not yet a supported stable install.

Start with the [stable product requirements](docs/product/stable-turnkey-product.md), [target architecture](docs/architecture/kubernetes-api-maturity.md), [replacement API](docs/api/replacement-api-proposal.md), and [project status](PROJECT_STATUS.md).

The old [PRD](docs/product/PRD.md) and [alpha API contract](docs/api/api-contract.md) are as-built evidence only. They are not compatibility inputs.

## License

Waycloak is licensed under the [MIT License](LICENSE). Apache-2.0 material retains its required notices.
