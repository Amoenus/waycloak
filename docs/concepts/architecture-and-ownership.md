# Architecture and ownership

The distribution owns `VPNGatewayClass`; the cluster/network operator owns `VPNGateway`; the workload owner owns `VPNEgressRoute` and the enrollment label; the controller exclusively owns `VPNWorkloadBinding`; and the privileged node agent exclusively owns node packet state.

Gateway credentials remain referenced by the gateway. The node agent receives no VPN credentials. Applications receive no Waycloak container, init container, capability, host mount, VPN credential, or Kubernetes credential. `Ready=True` is reserved for current, observed live data-plane health.

Cross-namespace attachment requires explicit gateway-owner consent and does not reveal whether an unauthorized target exists. Core currently accepts exactly one effective parent.
