# Replacement developer experience

Workload authors create a same-namespace `VPNEgressRoute` and place exactly one lookup label on the Pod template. The label is not a configuration bag. Old gateway, data-plane, adapter, and lease annotations are rejected.

Operators create a `VPNGateway` referencing an immutable `VPNGatewayClass` claim and same-namespace native configuration or Secret objects. The controller publishes positive-polarity `Accepted`, `ResolvedRefs`, `Programmed`, and `Ready` conditions with current `observedGeneration`; route status is per parent.

A failed CNI `ADD` prevents the sandbox from becoming runnable. Users diagnose the route, binding, gateway, and node-agent conditions; they never repair an enrolled workload by removing denial or selecting ordinary egress.

The signed preflight/install/doctor workflow and complete examples are the
supported release-candidate path documented in
[Getting started](../getting-started.md). Stable graduation remains owned by
issue #138 and requires its outstanding real-provider timing and certification
evidence.

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
