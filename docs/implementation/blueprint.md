# Implementation blueprint

This is a starting layout, not a substitute for incremental design. Keep packages aligned to product boundaries and avoid provider logic in reconcilers.

```text
api/v1alpha1/                 Go API types and generated schemas
cmd/controller/              controller manager and webhook process
cmd/agent/                   Pod network-namespace agent
cmd/gateway-manager/         gateway-local control process
internal/admission/          mutation and validation
internal/allocation/         stable address and lease allocation
internal/controller/         reconcilers
internal/dataplane/          interfaces for routes, links, firewall, DNS
internal/dataplane/linux/    Linux netlink/nftables implementation
internal/gateway/            desired/observed gateway model
internal/provider/           provider capability interfaces
internal/provider/gluetun/   Gluetun engine adapter
internal/provider/proton/    Proton NAT-PMP port-forward driver
internal/status/             conditions, reasons, and event helpers
config/crd/                  generated CRDs
config/rbac/                 generated and reviewed RBAC
charts/waycloak/             installation chart
test/e2e/                    Kind/k3d behavioral tests
test/fixtures/               fake engine/provider and network fixtures
docs/                        normative design and operations material
```

## Process boundaries

### Controller binary

One image may initially run reconcilers and admission endpoints. It needs Kubernetes API access but no networking capabilities or provider credentials.

### Agent binary

Runs as an injected container plus initial setup mode. The same binary can expose subcommands such as `prepare`, `run`, and `preflight`. It receives only allocation and gateway transport configuration. It needs `NET_ADMIN`; it should not mount a ServiceAccount token.

### Gateway-manager binary

Runs in the gateway Pod network namespace beside Gluetun. It observes the tunnel, programs overlay/NAT state, and operates provider leases. It receives provider control access and therefore has a higher trust level than the workload agent.

## Key interfaces

Illustrative Go interfaces:

```go
type DataPlane interface {
    InstallFailClosed(ctx context.Context, policy Policy) error
    ReconcileOverlay(ctx context.Context, desired Overlay) error
    Observe(ctx context.Context) (Observation, error)
    RemoveOwnedState(ctx context.Context, owner UID) error
}

type VPNEngine interface {
    Status(ctx context.Context) (TunnelStatus, error)
    PublicIP(ctx context.Context) (netip.Addr, error)
    Capabilities(ctx context.Context) (Capabilities, error)
}

type PortForwardProvider interface {
    Acquire(ctx context.Context, request LeaseRequest) (ProviderLease, error)
    Renew(ctx context.Context, lease ProviderLease) (ProviderLease, error)
    Release(ctx context.Context, lease ProviderLease) error
}
```

Interfaces should return typed observations and errors suitable for stable Kubernetes condition reasons. They must not expose raw credentials or provider response bodies in errors.

## Reconciliation rules

- Treat specs as desired state and status as observation.
- Use resource generations and idempotent operations.
- Never use list position as identity.
- Persist allocation before publishing it to an agent.
- Use server-side apply only with explicit field ownership.
- Make finalizers bounded and removable when external cleanup is impossible.
- Apply gateway rule updates atomically when the kernel API supports transactions.

## Development sequence

1. Scaffold APIs and controller with fake implementations.
2. Prove stable allocation, required per-Pod ConfigMap delivery, and admission behavior in envtest/Kind.
3. Implement a fake gateway that exposes a distinguishable egress endpoint.
4. Prove fail-closed semantics before integrating a real VPN.
5. Add Gluetun as an engine adapter.
6. Add provider port forwarding only after egress invariants pass.

This sequencing isolates Waycloak failures from provider behavior and makes CI possible without real VPN credentials.
