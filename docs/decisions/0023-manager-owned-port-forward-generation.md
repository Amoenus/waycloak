# ADR 0023: Gateway-manager-owned port-forward mapping generation

Status: Accepted
Date: 2026-07-19

## Context

The gateway manager acquires and renews short-lived provider mappings, while
the Kubernetes controller observes them and publishes status. The original
flow made the controller derive `leaseGeneration`, write it into the gateway
desired-state ConfigMap, and wait for the mounted ConfigMap update to return to
the manager before matching DNAT rules could be installed.

Mounted ConfigMaps are eventually projected by kubelet. Their propagation
delay can be comparable to Proton's 60-second NAT-PMP lifetime. A real-provider
run demonstrated the resulting circular dependency: the provider mapping
advanced, but gateway rules remained one generation behind across another
renewal. Increasing a test timeout would not make that loop converge.

Renewal also has two distinct outcomes. Extending the validity of the same
public address and port does not change the data-plane mapping. Reacquiring or
rotating either endpoint component does. Treating both as equivalent creates
avoidable application churn and obscures the point at which fail-closed
withdrawal is required.

## Decision

The gateway manager owns a monotonic mapping generation for each stable lease
UID. It acquires or renews the provider mapping and reconciles the matching
Waycloak-owned nftables rules from the same local observation in one control
loop. The Kubernetes controller observes and publishes that generation; it no
longer derives the generation or feeds it back through ConfigMap projection as
a prerequisite for rule installation.

The generation advances on first acquisition, reacquisition after expiry, or
a public-address or public-port change. An expiry-only renewal of the same
endpoint preserves the generation and updates only the lease timestamps. The
last controller-persisted generation and endpoint remain in desired state as a
restart hint. A restarted manager advances from that seed on fresh acquisition.
If an initially stale projection later reveals that the new endpoint reused a
controller-current generation, the manager advances it locally without another
provider request. The controller rejects generation regression and endpoint
changes within an already observed generation.

A failed renewal does not make a still-current observed mapping false. The
manager retains its rules, reports `renewalPending`, and retries while the last
provider expiry remains in the future. At expiry, or whenever the tunnel is not
observed ready, the effective rule generation becomes zero and the atomic
nftables reconciliation removes the lease rules. Gateway and lease readiness
then remain false until a current mapping and its exact rules are observed.

For delivery, an agent record with the same lease UID, Pod UID, generation,
and application port may retain an earlier unexpired deadline while an
expiry-only renewal propagates. Its deadline must never exceed the current
provider expiry. Generation or endpoint changes still require exact new rule,
delivery, and application acknowledgement before `Ready=True`.

Stable Kubernetes intent, target identity, internal port, and the restart seed
remain in the desired-state ConfigMap. It is not a fast signaling channel for
provider observations. Kubernetes Events remain supplemental diagnostics, not
control-loop state.

This ADR supersedes ADR 0013 only where it assigned generation calculation to
the controller and ADR 0014 only where rule installation depended on the
controller-published current generation. Their provider-ownership, identity,
atomicity, read-back, and fail-closed decisions remain in force.

## Consequences

- Provider mapping and matching gateway rules converge within one manager
  reconciliation instead of waiting for kubelet ConfigMap projection.
- Expiry-only renewals do not restart or reconfigure applications solely
  because timestamps changed.
- Temporary renewal errors are machine-observable without prematurely
  withdrawing a mapping that is still valid.
- Expiry, tunnel loss, endpoint rotation, and generation regression remain
  fail closed.
- The manager remains tokenless and the controller remains the sole writer of
  Kubernetes status.
- Sustained real-provider acceptance must cover expiry-only renewal, actual
  endpoint rotation, gateway replacement, and recovery on the same workload
  identity.

## Alternatives rejected

- Increase the controller or acceptance timeout: does not remove the circular
  dependency and can still lose to repeated short provider lifetimes.
- Watch Kubernetes Events: Events are best-effort diagnostics and do not
  replace durable level-based state.
- Add another operator framework: controller-runtime already provides the
  required reconciliation model; the defect was ownership and signaling.
- Make the gateway manager update Kubernetes status: would require an API token
  in the privileged gateway and expand its trust boundary.
- Advance generation for every successful renewal: causes needless rule and
  application churn when the endpoint did not change.
