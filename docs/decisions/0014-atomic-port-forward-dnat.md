# ADR 0014: Atomic port-forward DNAT ownership and observation

Status: Accepted
Date: 2026-07-13

## Context

A current provider mapping identifies a public port and a gateway-local
internal port, but it does not identify a safe Kubernetes destination. Inbound
traffic must reach only the exact Pod UID and overlay allocation observed by
the `PortForwardLease` controller. Target changes must not rotate the provider
mapping, and adding or removing another lease must not renumber or transiently
cross-deliver an existing lease. Status cannot infer rule readiness from
desired configuration alone.

The gateway already owns a dedicated IPv4 nftables table with a drop-policy
forward chain. Creating independent mutable rules per lease would expose
partially applied add, update, and delete sequences and complicate ownership.

## Decision

The gateway manager owns port-forward DNAT in its existing per-gateway IPv4
nftables table. Each reconcile deterministically sorts leases by object UID
and protocols, then replaces the complete owned table in one native nftables
transaction. It does not flush or modify unrelated tables.

For each active lease generation, prerouting matches the exact VPN tunnel
interface, transport protocol, and persisted provider internal port before
destination-NAT to the UID-bound overlay address and target port. A paired
forward rule admits only the post-DNAT protocol, address, and port from that
tunnel interface to the owned gateway VXLAN interface. The target overlay
address must also be present in current observed gateway membership; otherwise
the controller omits the intent and the next atomic replacement removes its
rules.

Both rules carry read-back metadata containing the lease UID, public-port
generation, protocol, overlay address, and target port. The gateway observation
reports rule readiness only when every expected marker appears in its exact
prerouting or forward chain after the transaction. The Kubernetes controller
sets `GatewayRulesReady=True` only when that observation also matches the
current provider mapping, `leaseGeneration`, and target status.

Provider mapping identity excludes target address, target port, and rule
generation. A target-only change therefore updates DNAT without releasing or
renewing the provider lease. `Delivered` and overall lease `Ready` remain false
until the separate ADR 0011 delivery record is implemented and observed.

## Consequences

- TCP and UDP for a shared provider port are installed and removed atomically.
- Removing one UID cannot rename or reindex another UID's rules.
- A stale target that is no longer an observed member loses its rules even if
  its old listener remains reachable on the overlay.
- Gateway rule status describes read-back state for an exact generation rather
  than successful desired-state publication.
- The gateway manager still needs only `NET_ADMIN`; it receives neither a
  Kubernetes API token nor application credentials.
- Host-level nftables administrators remain inside the privileged-node trust
  boundary and can override networking, as documented in the threat model.

## Alternatives rejected

- Append and delete individual lease rules: exposes partial updates and makes
  stale cleanup dependent on imperative history.
- Key rules by public port or list index: provider rotation or membership
  changes would rewrite stable identity.
- Release the provider mapping on target change: couples independent provider
  and Kubernetes lifecycles and creates avoidable inbound downtime.
- Treat a successful nftables write as controller readiness: skips the serving
  gateway read-back and can report a generation the gateway did not retain.
