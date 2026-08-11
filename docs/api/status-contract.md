# Stable status contract

Every reconciled replacement resource reports positive-polarity `Accepted`,
`ResolvedRefs`, `Programmed`, and `Ready` conditions after its first
reconciliation. Each condition carries the resource's current generation and a
concise, non-sensitive message. `Unknown` always means that observation is
unavailable and always uses reason `ObservationUnavailable`.

`Ready=True` is stricter than desired-state publication or registration. It is
valid only after the responsible component observes the complete live contract
for that resource. `Programmed=True` alone never permits application startup or
traffic.

## Stable condition vocabulary

| Resource | Diagnostic conditions | Stable positive/pending reasons |
| --- | --- | --- |
| `VPNGateway` | `TunnelReady`, `DNSReady`, `MembershipApplied` | `TunnelReady` / `TunnelNotReady`; `DNSReady` / `DNSNotReady`; `MembershipApplied` / `MembershipPending` |
| `VPNWorkloadBinding` | `NodeReady` | `NodeReady` / `NodeNotReady` |
| `PortForwardLease` | `GatewayRulesReady`, `Delivered`, `Acknowledged` | `GatewayRulesReady` / `GatewayRulesPending`; `Delivered` / `DeliveryPending`; `Acknowledged` / `AcknowledgementPending` |

Every diagnostic condition also permits `ObservationUnavailable` only with
status `Unknown`. The API server rejects a diagnostic type on the wrong kind and
rejects reasons outside the stable vocabulary.

## Generations and transitions

Controllers keep both `status.observedGeneration` and every condition's
`observedGeneration` current. Readers must treat a status or condition from an
older generation as unavailable. A transition timestamp changes only when the
condition status changes; a reason, message, or generation refresh with the
same status retains the existing timestamp.

## Ownership and writes

Status writers use the field-manager identities frozen in the replacement API
contract and server-side apply without force ownership. Summary conditions are
map entries keyed by type, so independent managers can own distinct entries.
Baseline route parent status is an at-most-one atomic entry identified by the exact
parent reference and immutable controller name; a competing manager receives a
conflict instead of taking it over.

Before writing, a controller compares the complete desired status with the
observed object. A semantic no-op performs no API write. Concurrent
reconciliation must converge to the same status and subsequent reconciliations
must preserve resource version and transition timestamps.
