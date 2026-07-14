# Install Waycloak

Waycloak releases are published as signed, digest-addressed OCI images, a
signed Helm OCI chart containing the served CRDs, and an optional signed KCL
OCI module. Verify the signed release manifest and every referenced artifact
before installation. Never substitute mutable tags for the recorded digests.

## Prerequisites

- Kubernetes 1.35 or 1.36 with Linux worker nodes and VXLAN support (`v0.1.0`
  is verified with Kindnet and Flannel; the chart's broader API-version check is
  not a compatibility claim);
- Helm 3.14 or newer;
- Cosign 2.6 or newer and `jq` for release verification;
- cluster-admin access for CRDs, admission webhooks, and cluster-scoped RBAC;
- OpenSSL for the initial webhook certificate;
- a CNI and node policy that permit the documented UDP 4789 overlay;
- explicit Pod Security exceptions described in [security exceptions](security-exceptions.md).

See [upgrade and rollback](upgrade.md) before changing any released image digest or webhook CA.

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

Download and verify the signed manifest. Its exact workflow identity binds the
artifact set to the protected release tag. The chart referenced by that
manifest already contains the released controller, agent, and gateway-manager
digests and all three CRDs.

```sh
version=v0.1.0
release_url="https://github.com/Amoenus/waycloak/releases/download/$version"
curl --fail --location --remote-name "$release_url/release-manifest.json"
curl --fail --location --remote-name "$release_url/release-manifest.sigstore.json"

identity="https://github.com/Amoenus/waycloak/.github/workflows/release.yaml@refs/tags/$version"
issuer=https://token.actions.githubusercontent.com
cosign verify-blob \
  --bundle release-manifest.sigstore.json \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  release-manifest.json

jq -r '.artifacts | .controllerImage.reference, .agentImage.reference, .gatewayManagerImage.reference, .qbittorrentAdapterImage.reference, .helmChart.reference, .kclModule.reference' \
  release-manifest.json | while IFS= read -r artifact; do
    cosign verify \
      --certificate-identity "$identity" \
      --certificate-oidc-issuer "$issuer" \
      "$artifact" >/dev/null
  done

chart_ref="$(jq -r '.artifacts.helmChart.reference' release-manifest.json)"
chart_name="${chart_ref%@*}"
expected_digest="${chart_ref##*@}"
pull_output="$(helm pull "oci://$chart_name" --version "${version#v}" 2>&1)"
printf '%s\n' "$pull_output"
actual_digest="$(printf '%s\n' "$pull_output" | awk '$1 == "Digest:" {print $2}')"
test "$actual_digest" = "$expected_digest"

helm upgrade --install waycloak "waycloak-${version#v}.tgz" \
  --namespace waycloak-system \
  --set webhook.tls.existingSecret=waycloak-webhook-tls \
  --set-string webhook.tls.caBundle="$ca_bundle" \
  --wait --timeout 5m
```

The signed manifest also records the source commit, tested Kubernetes/CNI
matrix, required capabilities, and pinned Gluetun identity. Keep it with the
deployment change record.

KCL users may add the optional module only after verifying that its semantic
tag resolves to `.artifacts.kclModule.digest`:

```sh
kcl mod add oci://ghcr.io/amoenus/waycloak-kcl --tag "${version#v}"
```

Commit the resulting `kcl.mod.lock`. KCL remains an authoring option; Helm is
the primary installer and the running Waycloak components have no KCL runtime
dependency.

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
