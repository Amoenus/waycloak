# Replacement developer experience

Workload authors create a same-namespace `VPNEgressRoute` and place exactly one lookup label on the Pod template. The label is not a configuration bag. Old gateway, data-plane, adapter, and lease annotations are rejected.

Operators create a `VPNGateway` referencing an immutable `VPNGatewayClass` claim and same-namespace native configuration or Secret objects. The controller publishes positive-polarity `Accepted`, `ResolvedRefs`, `Programmed`, and `Ready` conditions with current `observedGeneration`; route status is per parent.

A failed CNI `ADD` prevents the sandbox from becoming runnable. Users diagnose the route, binding, gateway, and node-agent conditions; they never repair an enrolled workload by removing denial or selecting ordinary egress.

The signed preflight/install/doctor workflow and complete examples remain owned by #138 and are not yet published as stable product guidance.
