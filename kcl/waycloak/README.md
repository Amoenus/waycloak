# Waycloak KCL package

This optional package contains generated KCL schemas for the clean-break
`networking.waycloak.io/v1beta1` API. The CRDs under `config/crd/bases` are the
single source of truth; `hack/generate-kcl-models.sh` reproduces these models.

The package contains schemas for `VPNGatewayClass`, `VPNGateway`,
`VPNEgressRoute`, `VPNWorkloadBinding`, `PortForwardLease`, and
`WorkloadAdapter`. `VPNWorkloadBinding` is controller-authored and is included
for validation and tooling, not as a user manifest surface.

Use the examples as API-only authoring references. This #128 intermediate does
not install a controller or data plane and must not be used to start enrolled
workloads. Pod enrollment is exactly one same-namespace route label:

```yaml
networking.waycloak.io/egress-route: private
```

There are no compatibility helpers for legacy annotations or objects.
