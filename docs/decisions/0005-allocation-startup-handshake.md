# ADR 0005: Controller allocation with fail-closed Pod startup

Status: Accepted
Date: 2026-07-13

## Context

The admission webhook sees a Pod before Kubernetes assigns its UID. The routing agent needs a collision-free, durable overlay allocation before application traffic starts. Mutating webhooks should avoid non-idempotent allocation side effects, and application Pods should not receive broad Kubernetes API credentials.

## Decision

Admission injects a required ConfigMap volume with a deterministic name derived from workload namespace and Pod name. The ConfigMap does not exist at admission time. After the Pod is stored, the controller observes its UID, creates a controller-owned `VPNWorkload`, persists a stable allocation, and creates the ConfigMap with an owner reference and UID binding.

Kubelet cannot start the injected init containers while the required ConfigMap is absent. The first init container installs fail-closed state and configures the overlay. A second init container verifies the protected path. Only then do the application and conventional long-running agent sidecar start.

## Consequences

- Controller outage delays protected Pod startup instead of allowing ordinary egress.
- Admission remains idempotent and does not allocate addresses.
- Application containers do not need Kubernetes API tokens.
- Same-name replacement requires the controller to reject/delete stale ConfigMaps owned by a different Pod UID.
- ConfigMap and registration cleanup require bounded owner/finalizer behavior.
- A brief gap before the conventional agent sidecar starts is safe because init-installed deny rules and a verified overlay already exist.

## Alternatives rejected

- Alphabetical or list-index allocation: renumbers existing clients.
- Address hashing without persisted collision handling: weak for small IPv4 pools.
- Admission-time allocation: side effects are difficult to make safe under retries and Pod rejection.
- Giving the application ServiceAccount registration-read RBAC: broadens application authority.
- Requiring native sidecar containers: unnecessarily narrows supported Kubernetes versions.
