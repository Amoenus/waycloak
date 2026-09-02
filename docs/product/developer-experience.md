# Replacement developer experience

Workload authors create a same-namespace `VPNEgressRoute` and place exactly one lookup label on the Pod template. The label is not a configuration bag. Old gateway, data-plane, adapter, and lease annotations are rejected.

For a platform-provided shared gateway, this is a normal two-change GitOps
workflow: add the local route and label the existing Pod template. No CLI is
required. The copyable YAML, Kustomize layout, KCL equivalent, and Kubernetes-
native verification are documented in
[GitOps workload onboarding](../guides/gitops-workloads.md).

Operators create a `VPNGateway` referencing an immutable `VPNGatewayClass` claim and same-namespace native configuration or Secret objects. The controller publishes positive-polarity `Accepted`, `ResolvedRefs`, `Programmed`, and `Ready` conditions with current `observedGeneration`; route status is per parent.

A failed CNI `ADD` prevents the sandbox from becoming runnable. Users diagnose the route, binding, gateway, and node-agent conditions; they never repair an enrolled workload by removing denial or selecting ordinary egress.

The signed preflight/install workflow is the supported privileged platform
lifecycle documented in [Getting started](../getting-started.md). `doctor` is a
convenient operational view, not a requirement for workload authoring.
The [configuration reference](../configuration.md) keeps operator-controlled
settings in one place, while the [Helm](../guides/helm.md) and
[KCL](../guides/kcl.md) guides show the primary installation and optional typed
authoring experiences. Stable `v1.0.1` qualification includes the exact
artifact, real-provider, lifecycle, fail-closed, and live homelab evidence
recorded in its [release notes](../releases/v1.0.1.md).

Port forwarding is application-neutral by default: an owner selects a stable
Service/backend port and Waycloak translates the changing public mapping at the
gateway. Gluetun remains responsible for VPN-provider support; Waycloak selects
a narrow engine capability only when the configured Gluetun provider and tunnel
mode require an external mapping protocol. Most workloads therefore need no
plugin or sidecar.

If an application cannot consume the stable port through a standard protocol or
neutral lease record, an integrator may supply an immutable, explicitly trusted
`WorkloadAdapter`. The adapter owns only application configuration and
generation acknowledgement. qBittorrent is the reference exception because it
must update and reannounce its public listener when the provider-assigned port
changes; it is not part of the generic install contract.
