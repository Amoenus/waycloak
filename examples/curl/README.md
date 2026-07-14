# Protected curl example

This disposable Deployment demonstrates the ordinary Waycloak experience: one
gateway annotation and no application-specific adapter.

Before applying it:

1. install Waycloak;
2. create a ready `waycloak-egress/proton-eu` gateway;
3. read the Pod Security implications in the
   [getting-started guide](../../docs/getting-started.md);
4. change the annotation if your gateway has another name.

```sh
kubectl apply -k examples/curl
kubectl wait --for=condition=Available deployment/waycloak-demo \
  --namespace waycloak-demo --timeout=5m
kubectl exec --namespace waycloak-demo deployment/waycloak-demo \
  --container curl -- \
  curl --fail --silent --show-error --output /dev/null \
  --write-out 'protected HTTPS status: %{http_code}\n' \
  https://example.com
```

The expected status is `200`. This proves DNS and HTTPS connectivity through
the selected path, not the identity of the VPN exit. Follow
[troubleshooting](../../docs/operations/troubleshooting.md) if the Pod remains
in init or becomes unready.

```sh
kubectl delete namespace waycloak-demo
```
