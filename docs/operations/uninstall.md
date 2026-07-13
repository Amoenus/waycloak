# Uninstall Waycloak

Removing the controller while protected Pods still exist leaves their deny-first network state in place and can leave application traffic unavailable. Migrate workloads deliberately before uninstalling.

1. Remove `networking.waycloak.io/gateway` from every workload Pod template and roll each workload.
2. Confirm no protected Pods or `VPNWorkload` objects remain.
3. Delete every `VPNGateway` and wait for its StatefulSet, Service, ConfigMap, and PodDisruptionBudget to disappear.
4. Uninstall the Helm release.
5. Delete CRDs only after confirming their instances are gone.

```sh
kubectl get pods -A -o jsonpath='{range .items[?(@.metadata.annotations.networking\.waycloak\.io/gateway)]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}'
kubectl get vpnworkloads,vpngateways -A
helm uninstall waycloak -n waycloak-system
kubectl delete crd vpngateways.networking.waycloak.io vpnworkloads.networking.waycloak.io
kubectl delete namespace waycloak-system
```

Helm intentionally does not delete CRDs, provider credential Secrets, the externally managed webhook TLS Secret, or namespaces. Delete those only after applying your organization's retention and credential-rotation policy.
