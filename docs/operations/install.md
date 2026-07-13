# Install Waycloak

Waycloak is pre-release software. Until signed OCI artifacts are published, build the three images from the same commit and install the chart from this repository. Never substitute mutable tags for the required digests.

## Prerequisites

- Kubernetes 1.30 or newer with Linux worker nodes and VXLAN support;
- Helm 3.14 or newer;
- cluster-admin access for CRDs, admission webhooks, and cluster-scoped RBAC;
- OpenSSL for the initial webhook certificate;
- a CNI and node policy that permit the documented UDP 4789 overlay;
- explicit Pod Security exceptions described in [security exceptions](security-exceptions.md).

## Prepare webhook TLS

The chart deliberately does not generate random certificates and has no cert-manager dependency. It consumes an existing `kubernetes.io/tls` Secret plus the matching base64-encoded CA bundle. For release name `waycloak` in namespace `waycloak-system`, the serving certificate must include `waycloak-webhook.waycloak-system.svc`.

```sh
namespace=waycloak-system
service=waycloak-webhook
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

kubectl create namespace "$namespace"
openssl genrsa -out "$workdir/ca.key" 3072
openssl req -x509 -new -key "$workdir/ca.key" -sha256 -days 3650 \
  -subj '/CN=waycloak-webhook-ca' -out "$workdir/ca.crt"
openssl genrsa -out "$workdir/tls.key" 3072
openssl req -new -key "$workdir/tls.key" \
  -subj "/CN=$service.$namespace.svc" -out "$workdir/tls.csr"
printf 'subjectAltName=DNS:%s.%s.svc,DNS:%s.%s.svc.cluster.local\nextendedKeyUsage=serverAuth\n' \
  "$service" "$namespace" "$service" "$namespace" >"$workdir/tls.ext"
openssl x509 -req -in "$workdir/tls.csr" -CA "$workdir/ca.crt" -CAkey "$workdir/ca.key" \
  -CAcreateserial -out "$workdir/tls.crt" -days 365 -sha256 -extfile "$workdir/tls.ext"
kubectl create secret tls waycloak-webhook-tls -n "$namespace" \
  --cert="$workdir/tls.crt" --key="$workdir/tls.key"
ca_bundle="$(base64 <"$workdir/ca.crt" | tr -d '\n')"
```

Keep the CA private key in an approved secret-management system if certificates will be renewed from it. Do not commit it or pass it to Helm.

## Install immutable artifacts

Obtain the controller, agent, and gateway-manager manifest digests produced from the same source commit. The chart schema rejects missing or non-SHA-256 identities.

```sh
helm upgrade --install waycloak ./charts/waycloak \
  --namespace waycloak-system \
  --set-string images.controller.digest="sha256:$CONTROLLER_DIGEST" \
  --set-string images.agent.digest="sha256:$AGENT_DIGEST" \
  --set-string images.gatewayManager.digest="sha256:$GATEWAY_MANAGER_DIGEST" \
  --set webhook.tls.existingSecret=waycloak-webhook-tls \
  --set-string webhook.tls.caBundle="$ca_bundle" \
  --wait --timeout 5m
```

Verify that two controller replicas are ready, the controller PDB permits at most one voluntary disruption, and both webhook configurations contain the `opted-in` match condition. Unannotated Pods are never sent to Waycloak admission.

## Create a gateway

Use a dedicated gateway namespace with the required security exception. Create provider credentials from files so values do not enter shell history:

```sh
kubectl create namespace waycloak-egress
kubectl create secret generic vpn-credentials -n waycloak-egress \
  --from-file=username=/secure/path/openvpn-username \
  --from-file=password=/secure/path/openvpn-password
kubectl label namespace applications networking.waycloak.io/access=allowed
kubectl apply -f config/samples/networking_v1alpha1_vpngateway.yaml
```

Wait for `VPNGateway` condition `Ready=True` before rolling an opted-in workload. The credentials Secret is mounted only into the VPN engine; application Pods receive neither the Secret nor a Kubernetes API token from Waycloak.

## Opt in a workload

Add this annotation to the Pod template, then roll the workload:

```yaml
networking.waycloak.io/gateway: waycloak-egress/example
```

Do not patch an already-running Pod. Admission and UID-bound allocation happen only while a new Pod is created.
