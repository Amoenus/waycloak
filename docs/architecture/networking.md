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

Forward and reverse paths must remain symmetric. DNAT rules are keyed by lease UID, not list index. Removing one lease cannot rewrite another lease's identity. A lease is `Active` only after provider allocation, gateway rule installation, target reachability, and driver health are observed.

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
