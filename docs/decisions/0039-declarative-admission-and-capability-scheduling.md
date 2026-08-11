# ADR 0039: Declarative admission and authenticated capability scheduling

Status: Accepted by issue #136

Date: 2026-07-28

## Context

An enrolled Pod should normally reach only a node that has the mandatory CNI,
authenticated node agent, supported backend, and exact release contract. This
placement is an operational safety and diagnostics layer. It cannot be the
packet-security boundary because admission can be missing or bypassed, node
labels can become stale, and hard node affinity is ignored after scheduling.

Kubernetes 1.36 makes `admissionregistration.k8s.io/v1`
`MutatingAdmissionPolicy` stable and enabled by default. It can add static
scheduling metadata without a webhook, service, certificate, or private key.
NodeRestriction also reserves node-label domains containing
`node-restriction.kubernetes.io` from kubelet mutation when that admission
plugin is enabled.

## Decision

The supported Kubernetes floor remains 1.36. A fail-closed
`MutatingAdmissionPolicy` adds this hard selector to every newly created Pod
carrying the one enrollment label:

```yaml
spec:
  nodeSelector:
    networking.waycloak.io.node-restriction.kubernetes.io/cni-ready: "true"
```

The JSON patch adds only that map key, so all workload-owner `nodeSelector` and
affinity constraints remain ANDed with it. A fail-closed
`ValidatingAdmissionPolicy` requires the selector after mutation and rejects
enrolled host-network/host-namespace Pods and direct `spec.nodeName` placement.
The existing validation of alpha annotations, route-name syntax, and immutable
live-Pod enrollment remains declarative. No admission webhook or admission TLS
infrastructure is installed, so there is no webhook timeout to configure.

The node agent reports readiness through the existing TLS, Pod-bound,
TokenReview-authenticated observation relay. A report contains a strict
version, exact Node name, boot and agent instance identities, observation time,
Baseline capabilities, immutable signed release identity, and conformance profile.
The controller accepts a report only for the authenticated agent Pod's assigned
Node and only when every value matches its own baseline contract. The node agent
keeps read-only Kubernetes RBAC. Only the controller can patch Nodes.

Agent readiness also requires a root-owned installation receipt produced by
the signed install plan. The receipt binds the exact release identity to the
SHA-256 digests of both the installed `waycloak-cni` binary and the active CNI
conflist. The agent mounts those three exact files read-only, verifies that the
conflist contains Waycloak exactly once after a primary plugin, and repeats the
verification on every reconciliation. Missing, writable, symlinked, changed,
release-skewed, or incorrectly chained artifacts force lockdown and a negative
capability report. The runtime agent never writes the CNI directories.

An accepted report publishes two controller-owned protected labels: the baseline
readiness selector and a server-time capability epoch. Missing capability,
unready backend, invalid installation receipt, clock rejection,
release/profile skew, or an authenticated negative report withdraws both. The
Node controller withdraws reports older
than 20 seconds. NodeRestriction is a stable-support preflight requirement;
without it a kubelet could spoof scheduling metadata, although it still cannot
make CNI or the local agent accept an unsafe attachment.

The CNI remains independently authoritative. If mutation is absent while
validation remains, enrolled Pod creation is denied because the required
selector is missing. If admission is entirely absent or stale, the chained CNI
still resolves the exact enrollment, installs lockdown, and refuses success on
a missing agent, rejected route, missing binding, capability/release skew, or
failed programming. A stale readiness label can therefore cause temporary
placement and sandbox failure, never ordinary egress or application startup.

Controller or relay loss makes every agent withdraw live allow paths and reject
new prepare operations. Labels may remain until the controller returns, but
the CNI refusal remains the creation-time boundary. Agent loss stops reports;
the label expires, and any assignment during the bounded window fails CNI ADD.
During upgrades, only exact controller/agent release identity is advertised;
skew deliberately causes denial until matching artifacts are running.

## Consequences

- Unsupported nodes leave enrolled Pods Pending with the standard scheduler
  `Unschedulable` condition and an event naming the missing CNI-ready label.
- Admission outages cannot create direct egress, and no admission service or
  certificate becomes part of product availability.
- A compromised kubelet cannot self-advertise on supported NodeRestriction
  clusters. Cluster administrators and node root remain trusted boundaries.
- Scheduling labels are a fast operator signal, not public capability APIs and
  not evidence of live packet-path readiness after a Pod has started.
- Exact release skew may reduce capacity during rolling upgrades; safety takes
  precedence over availability and no fallback node or data plane is selected.

## Alternatives rejected

- A dynamic admission webhook for node lookups: adds TLS and availability
  without improving the CNI enforcement boundary.
- Giving the node agent Node patch RBAC: expands a node compromise from local
  read authority to cluster scheduling mutation.
- Soft affinity or an ordinary kubelet-writable label: permits silent placement
  on an unsupported node.
- Scheduler-only enforcement: cannot protect API bypass, stale labels, agent
  loss, or packets after scheduling.

## Related decisions

- [ADR 0027](0027-explicit-workload-opt-in-and-attachment.md)
- [ADR 0032](0032-turnkey-bootstrap-and-preflight.md)
- [ADR 0034](0034-cni-creation-time-enforcement.md)
- [ADR 0035](0035-node-agent-trust-and-local-protocol.md)
- [ADR 0038](0038-binding-network-authority-and-observation-relay.md)
