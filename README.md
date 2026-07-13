# Waycloak

**Declarative private egress for Kubernetes.**

Waycloak is an implementation-stage Kubernetes project for routing explicitly opted-in workloads through shared VPN gateways. Its core promises are:

- one workload annotation selects a named private-egress gateway;
- protected traffic fails closed when the gateway is unavailable;
- many workloads can share one VPN tunnel without sharing a Pod;
- inbound provider port forwarding can be leased to multiple workloads;
- plain Kubernetes, Helm, Kustomize, and KCL users consume the same API;
- VPN credentials stay on the gateway and are never copied into application Pods.

Waycloak is based on a working homelab prototype using a VXLAN overlay, Gluetun, OpenVPN, Proton NAT-PMP port forwarding, and Kubernetes-native workload injection. This repository intentionally begins with contracts and acceptance criteria before production code.

## Intended experience

Annotate a Pod template:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: crawler
spec:
  template:
    metadata:
      annotations:
        networking.waycloak.io/gateway: private-egress/proton-eu
    spec:
      containers:
        - name: crawler
          image: example.invalid/crawler@sha256:REQUIRED_DIGEST
```

Waycloak injects the routing agent before the Pod starts, installs fail-closed policy, joins the selected gateway overlay, and reports readiness through Kubernetes conditions and events. Removing the annotation returns newly created Pods to ordinary cluster egress; it never silently changes the networking of a running Pod.

## Status

**Pre-alpha. The control plane, deny-first agent, and integrated Gluetun gateway path are implemented and cluster-tested. There are no released images yet; the source chart is for development validation until signed OCI artifacts are published.**

Implementation continues in roadmap order toward the first fail-closed data-plane proof. Start with:

1. [AGENTS.md](AGENTS.md)
2. [Product requirements](docs/product/PRD.md)
3. [Architecture](docs/architecture/architecture.md)
4. [Implementation roadmap](docs/implementation/roadmap.md)
5. [Project status](PROJECT_STATUS.md)
6. [Development installation](docs/operations/install.md)

## Project boundaries

Waycloak is not a VPN provider, a general-purpose service mesh, a replacement CNI, or a guarantee of anonymity. It orchestrates workload routing to operator-selected VPN engines and makes routing state observable and fail-closed.

## License

Waycloak is licensed under the [MIT License](LICENSE). Any Apache-2.0 material adapted from `angelnu/pod-gateway` must retain its upstream copyright and notice requirements.
