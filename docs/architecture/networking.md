# Replacement networking

Creation-time enforcement is mandatory for the baseline. After the primary CNI succeeds, the chained Waycloak plugin resolves the exact Pod UID and network namespace, installs lockdown, persists exact attachment identity, waits a bounded time for the UID-bound `VPNWorkloadBinding`, and asks the authenticated node agent to program and verify the protected path. Any failure keeps denial in place and makes CNI `ADD` fail, so application containers do not start.

The node agent owns nftables and netlink state, drift repair, withdrawal, restart recovery, and live observations. It receives neither VPN nor Kubernetes workload credentials. Controller, route, binding, gateway, tunnel, DNS, or observation loss withdraws the allow path while retaining the base deny.

The baseline packet matrix includes TCP, UDP, DNS over UDP and TCP, and fragmentation. Ordinary egress is permitted only for Pods that were never enrolled. See [ADR 0034](../decisions/0034-cni-creation-time-enforcement.md) and the [CNI feasibility evidence](../implementation/cni-creation-time-feasibility.md).
