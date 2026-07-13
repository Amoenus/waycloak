# ADR 0009: Gluetun firewall and Waycloak forwarding ownership

Status: Accepted
Date: 2026-07-13

## Context

Gluetun and the Waycloak gateway manager share one Pod network namespace. Gluetun deliberately sets the IPv4 `INPUT`, `OUTPUT`, and `FORWARD` policies to `DROP` so its local processes cannot bypass the VPN. Waycloak must preserve that engine kill switch while forwarding protected overlay traffic through the engine tunnel.

Gluetun does not expose a control endpoint or setting that delegates only forwarded traffic. Its supported post-firewall rules file is the narrow integration point available at startup. An `accept` rule in an independent nftables base chain cannot override a later `DROP` policy in Gluetun's filter chain.

## Decision

Gluetun continues to own local `INPUT` and `OUTPUT` policy, VPN-server reachability, and tunnel-loss containment. The controller supplies a static, non-secret Gluetun post-rules file that:

- changes only Gluetun's IPv4 `FORWARD` policy to `ACCEPT`;
- permits UDP/TCP DNS and TCP readiness input only from the configured overlay CIDR on the deterministic Waycloak VXLAN interface.

Waycloak owns a separate, deterministically named IPv4 nftables table. Before creating the VXLAN interface, the manager installs or verifies a base `forward` chain with policy `DROP`. It activates forwarding atomically only after both the overlay and the configured VPN interface are up. Active rules permit overlay-source traffic only from the owned VXLAN interface to the VPN interface, permit connection-tracked return traffic, and masquerade the overlay source only on the VPN interface.

The controller fixes Gluetun's `VPN_INTERFACE` to `wayvpn0`, so OpenVPN and WireGuard do not require provider-specific packet-policy names. The manager retains no Kubernetes credentials and uses native nftables/netlink APIs for every dynamic rule and interface operation.

## Consequences

- There is no startup interval in which a Waycloak-created VXLAN can forward through the ordinary CNI default route without the owned drop chain.
- Tunnel removal or interface mismatch leaves the owned forward chain at `DROP`; it cannot fall back to the underlay interface.
- Gluetun upgrades must be tested against the post-rules file contract and iptables backend behavior.
- The static adapter is visible in the controller-owned ConfigMap and contains no credentials, provider endpoints, or dynamic member identities.
- If Gluetun adds a supported forward-policy delegation API, this ADR should be superseded and the static post-rules adapter removed.

## Alternatives rejected

- Disable Gluetun's firewall and let Waycloak own all output policy: Waycloak does not have the provider server addresses needed to open the tunnel without a direct-egress window.
- Add an independent nftables `accept` chain while leaving Gluetun's `FORWARD` policy at `DROP`: an accept verdict in one base chain does not bypass a later drop verdict.
- Insert dynamic rules into Gluetun-owned chains: this creates unstable cross-owner handles and unsafe cleanup semantics.
- Shell out from the gateway manager to mutate iptables: dynamic behavior remains native and confined to the Waycloak-owned nftables table.
