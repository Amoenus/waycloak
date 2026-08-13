# Replacement developer experience

Workload authors create a same-namespace `VPNEgressRoute` and place exactly one lookup label on the Pod template. The label is not a configuration bag. Old gateway, data-plane, adapter, and lease annotations are rejected.

Operators create a `VPNGateway` referencing an immutable `VPNGatewayClass` claim and same-namespace native configuration or Secret objects. The controller publishes positive-polarity `Accepted`, `ResolvedRefs`, `Programmed`, and `Ready` conditions with current `observedGeneration`; route status is per parent.

A failed CNI `ADD` prevents the sandbox from becoming runnable. Users diagnose the route, binding, gateway, and node-agent conditions; they never repair an enrolled workload by removing denial or selecting ordinary egress.

The signed preflight/install/doctor workflow and complete examples are the
supported release-candidate path documented in
[Getting started](../getting-started.md). Stable graduation remains owned by
issue #138 and requires its outstanding real-provider timing and certification
evidence.
