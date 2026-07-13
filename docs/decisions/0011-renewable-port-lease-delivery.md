# ADR 0011: Renewable port-lease delivery and environment-only applications

Status: Accepted
Date: 2026-07-13

## Context

Provider-assigned forwarded ports can change during renewal or gateway
replacement. Kubernetes injects environment variables only when a container
starts, so an environment variable cannot represent renewable lease state.
Silently restarting a Deployment, StatefulSet, or arbitrary owner from the
Waycloak controller would cross an ownership boundary, can disrupt unrelated
containers, and cannot guarantee that an application consumed the new value.

Applications nevertheless need an API-neutral record and some legacy
applications accept configuration only through startup environment variables.
Lease delivery must not grant an application Kubernetes API credentials or
leave a process running with a stale lease while status claims delivery is
ready.

## Decision

The canonical live lease representation is versioned JSON delivered through an
atomically replaced read-only file and a read-only Pod-loopback HTTP endpoint.
Both surfaces contain the same lease identity, public port, protocols, gateway,
generation, issue/renewal/expiry times, and observed state. The Pod-local
Waycloak agent obtains only the lease records authorized for that Pod and does
not expose Kubernetes credentials to application containers.

Environment variables are an adapter behavior, not a live core API. An
application that reads values only at startup must explicitly run under a
Waycloak environment supervisor. The supervisor waits for a current ready
record, exports the documented variables, and starts the application as its
child. When the lease generation changes, expires, or becomes unready, the
supervisor stops the child. It starts a replacement only after a current ready
record is available, using the new generation. If the record disappears or is
invalid, the child remains stopped.

The controller does not mutate owner Pod templates, delete Pods, or restart
arbitrary workload controllers to refresh environment variables. Application
integrators opt into the supervisor in their workload command or use an
application-specific adapter that consumes the file or endpoint. `Delivered`
and `Ready` are based on acknowledgement of the current generation where the
selected adapter supports acknowledgement; merely writing desired lease state
does not imply readiness.

The Phase 4 API preserves these semantics through explicit container selection
and neutral adapter surfaces. It does not add a static environment-variable
projection that can silently become stale.

## Implementation

The Phase 4 neutral surface uses the optional Pod annotation
`networking.waycloak.io/port-forward-container: <container>`. Admission mounts
a dedicated ConfigMap volume filtered to `port-forward-leases.json` at
`/run/waycloak/port-forward` in only that application container. The shared
routing agent mounts the complete allocation ConfigMap, validates the document,
and serves it on loopback port 9809. Its existing health port exposes an
identity-specific delivery observation to the controller.

The controller publishes only records whose target Pod UID, provider mapping,
and gateway rules are all currently observed. A deterministic internal digest
annotation prompts kubelet to refresh the projected volume on renewal without
restarting the Pod. `Delivered=True` requires an exact readback of API version,
lease UID, Pod UID, generation, and unexpired Kubernetes-canonical expiry from
the target agent. This acknowledges the neutral delivery transport; application
adapters may add stronger application-specific acknowledgement. The
application container receives no allocation internals, capabilities, or
Kubernetes API token.

## Consequences

- Native integrations can react to file replacement or poll/watch the local
  endpoint without process restarts.
- Environment-only applications incur an explicit, observable child-process
  restart when the provider changes the lease.
- The application owner retains control over restart semantics and container
  selection.
- Lease loss stops supervised listeners but does not weaken the independent
  fail-closed egress policy.
- Delivery acknowledgement can distinguish current application consumption
  from controller registration.

## Alternatives rejected

- Inject the port as a Kubernetes environment variable: values never update in
  a running container.
- Have the controller roll the owning workload: crosses ownership and policy
  boundaries and is ambiguous for custom or multiply owned Pods.
- Keep the process running with a stale environment value: hides delivery
  failure and can advertise an invalid listener indefinitely.
- Give the application a ServiceAccount token to watch `PortForwardLease`:
  expands application privileges and couples it to Kubernetes internals.
- Put application-specific APIs in the controller: makes provider-neutral core
  behavior depend on each application's configuration model.
