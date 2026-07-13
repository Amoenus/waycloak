# Architecture decision records

ADRs capture decisions that are expensive to reverse. Accepted ADRs are normative until superseded.

- [0001: Shared gateway over per-workload VPN sidecars](0001-shared-gateway.md)
- [0002: Kubernetes-native API with admission injection](0002-kubernetes-api-and-admission.md)
- [0003: Gluetun as the first engine behind a provider interface](0003-gluetun-provider-interface.md)
- [0004: Helm OCI as the primary distribution](0004-helm-oci-distribution.md)
- [0005: Controller allocation with fail-closed Pod startup](0005-allocation-startup-handshake.md)
- [0006: Native nftables and netlink data-plane backend](0006-native-linux-data-plane.md)
- [0007: Transparent gateway-routed DNS](0007-transparent-gateway-dns.md)
- [0008: Gateway credential and engine-control boundary](0008-gateway-credential-and-engine-control-boundary.md)

New ADRs use the next number and include status, context, decision, consequences, alternatives, and supersession links.
