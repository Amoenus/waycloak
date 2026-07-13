# Dependencies

Waycloak keeps the mandatory dependency set small and makes homelab tooling optional.

## Cluster prerequisites

- A supported Kubernetes release on Linux.
- CNI connectivity that permits the configured VXLAN UDP port between protected Pods and gateway Pods.
- Linux TUN, VXLAN, netfilter connection tracking, and NAT support.
- `/dev/net/tun` available to gateway Pods.
- Permission to grant `CAP_NET_ADMIN` to Waycloak agent and gateway networking containers.
- A Pod Security admission strategy that narrowly exempts these Waycloak containers/namespaces.
- A standard Kubernetes Secret containing provider credentials.

## Product runtime

### First-party

- controller and admission webhook;
- routing/lease-delivery agent;
- gateway manager and provider driver;
- CRDs and RBAC installed by the Helm chart.

### Third-party

- Gluetun as the initial VPN engine;
- a compatible VPN provider account;
- OpenVPN or WireGuard support provided by the engine and provider;
- provider port-forward support only when inbound leases are requested.

Gluetun is pinned by digest in tested release metadata. It should eventually be replaceable through a driver/engine interface.

## Go implementation candidates

Exact module selection requires an ADR and maintenance review. Expected categories include:

- `sigs.k8s.io/controller-runtime` and Kubernetes API libraries;
- netlink operations, likely `github.com/vishvananda/netlink` or direct rtnetlink;
- nftables operations, likely `github.com/google/nftables`;
- structured logging through `log/slog` or controller-runtime-compatible logging;
- Prometheus client only if metrics are not supplied adequately by controller-runtime;
- testing with Ginkgo/Gomega or standard Go testing, envtest, and Kind.

Avoid introducing a database, message broker, service mesh, or separate policy engine for the initial product.

## Optional integrations

These are compatible but never required by the controller:

- External Secrets Operator, SOPS, Sealed Secrets, or Vault for Secret creation;
- Argo CD or Flux for GitOps;
- Crossplane for platform composition;
- KCL for trait-based authoring;
- cert-manager for webhook certificates;
- Prometheus Operator and Grafana for monitoring;
- Zot, Harbor, GHCR, or another OCI distribution registry.

## Build and release toolchain

- Go toolchain;
- Helm;
- Melange and APKO/Wolfi for minimal hardened images;
- Cosign for keyless artifact signing;
- Syft for SBOM generation;
- Grype or Trivy for vulnerability policy;
- SLSA-compatible provenance generation;
- GitHub Actions initially;
- KCL CLI only for the optional KCL OCI module.

Actions must be pinned by commit SHA. Build dependencies and base images must be pinned and updated through reviewed automation.

## Upstream inspiration and licensing

The VXLAN data-plane design is informed by [`angelnu/pod-gateway`](https://github.com/angelnu/pod-gateway) and admission concepts by [`angelnu/gateway-admision-controller`](https://github.com/angelnu/gateway-admision-controller), both Apache-2.0 projects.

Conceptual reimplementation does not require a code dependency. If code or scripts are copied or adapted, add the required Apache-2.0 license and NOTICE attribution before the change is merged. The Waycloak project remains MIT licensed while preserving third-party notices for included material.

## Explicitly not required

- KCL
- Crossplane
- Argo CD
- External Secrets Operator
- a replacement CNI
- a service mesh
- one VPN sidecar per application
- co-locating protected applications in the gateway Pod
