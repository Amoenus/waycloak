# Stable operational visibility

Waycloak publishes a bounded Prometheus projection of Kubernetes-native state
from the replacement controller. Metrics help operators find availability and
protection failures; they do not authorize packets, prove a tunnel, or replace
resource Conditions and Events.

## Metric contract

| Metric | Stable labels | Meaning |
| --- | --- | --- |
| `waycloak_resources` | `resource` | Current replacement API object count by kind. |
| `waycloak_resource_condition_objects` | `resource`, `condition`, `status`, `reason`, `current` | Aggregate object count for `Accepted`, `ResolvedRefs`, `Programmed`, `Ready`, gateway tunnel/DNS/membership, binding node, and lease delivery conditions. `current` is true only when `observedGeneration` matches. |
| `waycloak_enrolled_pods` | `state` | Explicitly enrolled Pods in `awaiting_capable_node`, `binding_absent`, `fail_closed`, `ready`, or `terminating` state. |
| `waycloak_workload_allocations` | `state` | Durable allocation reservations in `active`, `quarantined`, or `invalid` state. |
| `waycloak_metrics_collection_success` | `source` | Whether the current scrape could list each bounded Kubernetes source. A failed source emits zero and its dependent state is omitted rather than invented. |
| `controller_runtime_reconcile_errors_total` | controller-runtime contract | Reconciliation errors emitted by controller-runtime. |

The common `Accepted` projection covers the domain acceptance decision for
reconciled resources. Static Kubernetes admission can reject a request before
an object exists, so a controller cannot truthfully reconstruct or count that
rejected object. Use API-server audit records for individual static-policy
rejections; use `waycloak_enrolled_pods` for Pods that were admitted with the
explicit enrollment label.

Condition reasons are limited to the published stable API reason set. An
unknown reason is reported as `Other`, and a missing expected condition as
`ConditionAbsent`. Metrics never carry namespace, object name, UID, Pod name,
node, address, port, endpoint, provider, release digest, credential, or
free-form status message labels.

## Enable and scrape

The controller metrics endpoint is enabled by default and can be disabled:

```yaml
observability:
  metrics:
    enabled: true
    port: 8080
```

The chart exposes the named `metrics` port on the controller Service. The
endpoint is unauthenticated inside the cluster and must not be exposed through
an Ingress or public load balancer. Apply namespace-level network controls when
tenant access to aggregate operational state is not acceptable.

[`config/observability/prometheus-scrape.yaml`](../../config/observability/prometheus-scrape.yaml)
is a plain Kubernetes endpoint-discovery fragment. It requires Prometheus but
does not require Prometheus Operator, a `ServiceMonitor`, or a `PodMonitor`.
A scrape interval of 30 seconds or longer is recommended because each scrape
collects a current Kubernetes-state projection with a bounded five-second
deadline.

## Alert rules and dashboard

Set `observability.assets.enabled=true` to publish two optional ConfigMaps
whose names use the Helm release fullname:

- `<fullname>-prometheus-rules` contains plain Prometheus rules;
- `<fullname>-grafana-dashboard` contains the aggregate Grafana dashboard JSON.

The rules label every data-plane alert with `traffic_posture: fail_closed`.
`failure_domain: protection` means an enrolled workload is denied before a
protected path is live, including rejected or stale resource acceptance.
`failure_domain: availability` means gateway, tunnel, DNS, allocation,
delivery, or controller availability is degraded while the deny posture must
remain. `failure_domain: observability` means the metric projection itself is
incomplete and makes no traffic-posture claim.

Do not page from `Ready` alone without checking `current="true"`. For incident
diagnosis, inspect the affected resource Conditions and Events and use packet
evidence where the fail-closed invariant itself is in question.
