# Replacement implementation blueprint

```text
api/v1beta1/                  replacement Kubernetes API types
cmd/replacement-controller/  replacement-only controller manager
cmd/waycloak-cni/             mandatory chained CNI plugin
cmd/waycloak-node-agent/      privileged per-node runtime
internal/controller/          replacement reconcilers only
internal/cni/                 CNI lifecycle and authenticated client
internal/nodeagent/           programming, observation, drift and recovery
charts/waycloak/              replacement CRDs, RBAC and static admission
```

The removed alpha API, mutation webhook, Pod sidecar/init runtime, projected ConfigMap handshake, gateway manager, adapter protocol, and alpha release workflow must not reappear. `go run ./hack/alphaaudit` enforces this boundary.
