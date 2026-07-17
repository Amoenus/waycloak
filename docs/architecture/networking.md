# Networking design

## Data path

Each protected Pod and gateway share an overlay subnet implemented with VXLAN. The agent changes the Pod's external route to the gateway overlay address. The gateway forwards overlay traffic into the VPN tunnel and applies source NAT as required by the engine/provider.

```text
application socket
  -> Pod network namespace fail-closed rules
  -> Waycloak VXLAN interface
  -> CNI transport between nodes
  -> gateway VXLAN interface
  -> VPN tunnel interface
  -> VPN provider
  -> destination
```

The CNI interface remains necessary as VXLAN underlay transport. Fail-closed policy therefore cannot simply disable the CNI default route; it must allow only gateway-underlay and explicitly permitted cluster destinations through it.

## Initial defaults

Defaults are chart values, not hardcoded contracts:

- VXLAN UDP port: configurable, default `4789`;
- VNI: allocated per gateway from a configured range;
- overlay CIDR: configured per gateway;
- gateway overlay address: first usable address by convention;
- client allocations: durable and never name-order-derived;
- MTU: discovered or configured, with VXLAN and VPN overhead accounted for.

The controller rejects overlapping overlay CIDRs and duplicate VNIs within its managed scope where it can detect them.

## Fail-closed policy

The agent creates a Pod-UID-derived nftables table and protocol-tagged policy-routing rules with recognizable ownership. The main CNI routing table remains intact. A dedicated Waycloak table carries the protected default route, while higher-priority destination rules select the main table only for the observed VXLAN endpoint and declared `Preserve` CIDRs. The sequence is:

1. install external-egress deny policy;
2. permit overlay underlay traffic to the selected gateway endpoint;
3. permit configured cluster-local CIDRs and Kubernetes API access only when policy allows;
4. establish VXLAN;
5. direct external routes through the overlay;
6. configure protected DNS;
7. verify gateway and external egress;
8. report readiness.

On any error, the deny policy stays in place. Cleanup only removes objects carrying the current Waycloak ownership identity and must not flush application or CNI firewall state.

The lockdown and verifier init containers both require `CAP_NET_ADMIN`; application containers are never modified to receive it. The verifier requires both the owned kernel state and a successful TCP probe to the gateway's observed overlay health endpoint. Desired gateway registration, a present VXLAN link, or a reachable underlay endpoint alone cannot unblock application startup.

The long-running agent exposes only an HTTP readiness bit on port `9808` inside the Pod network namespace. It returns ready after the latest configuration load and owned-state repair both succeed, and immediately returns unready after either fails. Kubelet probes the Pod IP; binding the endpoint only to container loopback would incorrectly direct a kubelet probe at node loopback. This readiness signal describes the observed protected path but does not replace the API-level gateway and workload health observations required in Phase 3.

## Cluster-local modes

A gateway can choose one of these explicit policies:

- `Preserve`: cluster-local CIDRs stay directly reachable while internet egress uses the VPN.
- `Gateway`: all non-control traffic, including cluster-local destinations, traverses the gateway.
- `Deny`: cluster-local traffic is denied except declared destinations.

`Preserve` is the ergonomic initial default, but its CIDRs must be discovered reliably or configured. Documentation must state that cluster-local traffic does not pass through the VPN in this mode.

## DNS

Default behavior transparently destination-NATs every UDP/TCP port 53 request to a resolver on the gateway overlay, preserving kubelet-generated nameservers and search domains while preventing the selected resolver address from bypassing the gateway. The resolver's external upstream traffic leaves through the VPN tunnel. Encrypted-DNS bypass is not generally distinguishable from arbitrary TLS without domain policy; Waycloak guarantees that all external traffic, including DoH, still follows the protected route.

The gateway resolver forwards Kubernetes cluster zones to cluster DNS and external zones through the protected upstream. Acceptance covers service names, search domains, UDP, TCP fallback, and gateway outage. Missing resolver state is fail-closed and is repaired with the rest of the Pod-UID-owned nftables table.

The initial Gluetun integration fixes the engine tunnel interface name to `tunwaycloak`. The `tun` prefix is required for OpenVPN to infer the device type, and the alphanumeric spelling satisfies Gluetun's interface-name validation while retaining one deterministic name across the manager and engine. Gateway forwarding is installed deny-first in a dedicated IPv4 nftables table: a drop-policy forward chain exists before VXLAN, activation permits only owned-overlay source traffic toward `tunwaycloak`, return traffic must be connection-tracked, and masquerade applies only on `tunwaycloak`. A separate manager-owned inet input chain drops UDP 4789 before membership is valid and admits only observed member underlay IPs once configured; Gluetun's static rule is only the later handoff through its INPUT kill switch. Gluetun retains all other local input/output kill-switch ownership as defined by ADR 0009. Application port-53 traffic is transparently sent to the manager's overlay-only port 1053; the split proxy uses Gluetun's wildcard port-53 resolver only for external names and forwards cluster suffixes to observed cluster DNS. A destination-specific manager-owned policy rule and host route send only that observed resolver through the CNI default gateway ahead of Gluetun's outbound and half-default routes.

## Inbound port forwarding

When a provider lease is active:

```text
peer
  -> provider public IP:leased-port
  -> VPN tunnel
  -> gateway prerouting
  -> lease-specific TCP/UDP DNAT
  -> protected Pod overlay address:target-port
```

Forward and reverse paths must remain symmetric. DNAT rules are keyed by lease UID, not list index. Removing one lease cannot rewrite another lease's identity. A lease is `Ready` only after provider allocation, gateway rule installation, target binding, and delivery are observed.

The initial Proton/OpenVPN driver derives the NAT-PMP peer as address `.1` in
the observed IPv4 tunnel prefix, with an explicit operator override retained
for providers that require it, and sends from a socket bound to
`tunwaycloak`. Kubernetes status persists the unique NAT-PMP
internal port; the provider-assigned public port may rotate independently.
Waycloak renews at 75 percent of the returned lifetime and increments the
public lease generation only on rotation. Gluetun's own NAT-PMP loop remains
off to avoid competing owners. These mappings do not admit traffic until the
separate UID-keyed DNAT generation is installed and observed.
When a provider capability reports that requested external ports are
unsupported, acquisition and renewal send a zero public-port suggestion; the
stable internal port is the mapping identity. A previously observed public
port is suggested only to drivers that explicitly support requested ports.

The gateway manager installs active generations in the same Waycloak-owned
IPv4 nftables table as gateway forwarding. One transaction replaces the table
with deterministically ordered rules. Prerouting matches the exact VPN tunnel
interface, provider internal port, and TCP or UDP before DNAT to the current
UID-bound overlay address and target port. The corresponding forward rule
admits only that post-DNAT destination from the tunnel to the owned VXLAN
interface. A target must remain in current observed gateway membership.
Read-back markers include lease UID, generation, protocol, and target and must
appear in their exact prerouting and forward chains before
`GatewayRulesReady=True`. Removing a generation removes both rules atomically;
unrelated nftables tables are outside this ownership boundary.

## Ports and NetworkPolicy

The chart must document or generate NetworkPolicies for:

- Kubernetes API access from the controller;
- webhook traffic from the API server/control plane;
- VXLAN UDP between protected Pods and gateways;
- gateway VPN-provider egress;
- DNS and health endpoints;
- optional metrics scraping.

NetworkPolicy enforcement happens outside a Pod network namespace and may interact with encapsulation differently across CNIs. Supported CNIs require end-to-end validation.

## Kernel and security prerequisites

- Linux TUN device on gateway nodes;
- VXLAN kernel support;
- netfilter connection tracking and NAT;
- nftables or supported iptables compatibility;
- `CAP_NET_ADMIN` for routing agent and gateway networking containers;
- narrowly scoped Pod Security admission exemptions.

Waycloak must publish a preflight Job or CLI check that reports these prerequisites without changing cluster networking.

## Recovery and drift

The agent monitors link state, gateway liveness, desired generation, routes, and owned firewall objects. It rereads desired configuration and repairs in process. A ConfigMap update should not require application restart. Gateway configuration changes should be applied incrementally and should not restart the VPN tunnel for ordinary membership changes.

Engine readiness and engine recovery are separate controls. For Gluetun,
Waycloak probes the loopback-only health server from inside the engine
container. Readiness fails on the first unhealthy observation so the composite
gateway and every protected workload remain fail closed. A startup probe allows
five minutes for initial provider establishment. After startup, twelve
consecutive ten-second liveness failures restart only the engine container.
The gateway Pod, manager, overlay allocations, lease identities, and protected
workload Pods remain in place while the engine re-establishes the tunnel.

The liveness delay is intentionally longer than readiness failure: short
provider or DNS disturbances withdraw service without creating a restart
storm, while a live-but-stuck engine is recovered without an operator deleting
the singleton gateway Pod. Engines without an equivalent local health contract
do not inherit the Gluetun probe.

Observed public-IP metadata is telemetry, not a data-plane dependency. A
missing or malformed Gluetun public-IP response does not make the gateway
unready when the tunnel and protected DNS path are healthy. Tunnel health, DNS,
overlay reconciliation, forwarding policy, and provider lease/rule observations
remain mandatory readiness inputs.
