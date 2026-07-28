#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

readonly cluster_name="waycloak-turnkey-ci"
readonly registry_name="waycloak-turnkey-registry"
readonly registry_port="5001"
readonly registry_host="waycloak-registry.invalid"
readonly registry_image="registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
readonly kind_node_image="kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5"
readonly gluetun_ref="docker.io/qmcgaw/gluetun@sha256:6b54856716d0de56e5bb00a77029b0adea57284cf5a466f23aad5979257d3045"
readonly pause_ref="registry.k8s.io/pause@sha256:278fb9dbcca9518083ad1e11276933a2e96f23de604a3a08cc3c80002767d24c"
readonly release_version="v0.0.0-turnkey-ci"
readonly system_namespace="waycloak-system"
readonly release_name="waycloak"

work_dir="$(mktemp -d)"

cleanup() {
  kind delete cluster --name "$cluster_name" >/dev/null 2>&1 || true
  docker rm --force "$registry_name" >/dev/null 2>&1 || true
  rm -rf -- "$work_dir"
}
trap cleanup EXIT

mkdir -p "$work_dir/registry-tls"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$work_dir/registry-tls/ca.key" \
  -out "$work_dir/registry-tls/ca.crt" \
  -subj "/CN=Waycloak turnkey CI registry CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1
cat >"$work_dir/registry-tls/server.conf" <<EOF
[req]
distinguished_name = subject
req_extensions = extensions
prompt = no
[subject]
CN = ${registry_host}
[extensions]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:${registry_host},DNS:${registry_name}
EOF
openssl req -newkey rsa:2048 -nodes \
  -keyout "$work_dir/registry-tls/tls.key" \
  -out "$work_dir/registry-tls/tls.csr" \
  -config "$work_dir/registry-tls/server.conf" >/dev/null 2>&1
openssl x509 -req -days 1 -sha256 \
  -in "$work_dir/registry-tls/tls.csr" \
  -CA "$work_dir/registry-tls/ca.crt" \
  -CAkey "$work_dir/registry-tls/ca.key" \
  -CAcreateserial \
  -out "$work_dir/registry-tls/tls.crt" \
  -extfile "$work_dir/registry-tls/server.conf" \
  -extensions extensions >/dev/null 2>&1
cat /etc/ssl/certs/ca-certificates.crt "$work_dir/registry-tls/ca.crt" \
  >"$work_dir/registry-tls/ca-bundle.crt"
export SSL_CERT_FILE="$work_dir/registry-tls/ca-bundle.crt"
printf '127.0.0.1 %s\n' "$registry_host" | sudo tee -a /etc/hosts >/dev/null

docker run --detach --restart=always \
  --publish "127.0.0.1:${registry_port}:5000" \
  --name "$registry_name" \
  --env REGISTRY_HTTP_TLS_CERTIFICATE=/certs/tls.crt \
  --env REGISTRY_HTTP_TLS_KEY=/certs/tls.key \
  --volume "$work_dir/registry-tls/tls.crt:/certs/tls.crt:ro" \
  --volume "$work_dir/registry-tls/tls.key:/certs/tls.key:ro" \
  "$registry_image"

cat >"$work_dir/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
EOF

kind create cluster \
  --name "$cluster_name" \
  --image "$kind_node_image" \
  --config "$work_dir/kind.yaml" \
  --wait 120s

docker network connect kind "$registry_name"
registry_dir="/etc/containerd/certs.d/${registry_host}:${registry_port}"
for node in $(kind get nodes --name "$cluster_name"); do
  docker exec "$node" mkdir -p "$registry_dir"
  docker cp "$work_dir/registry-tls/ca.crt" "$node:$registry_dir/ca.crt"
  cat <<EOF | docker exec --interactive "$node" cp /dev/stdin "$registry_dir/hosts.toml"
server = "https://${registry_host}:${registry_port}"
[host."https://${registry_name}:5000"]
  capabilities = ["pull", "resolve"]
  ca = "${registry_dir}/ca.crt"
EOF
done

publish_image() {
  local name="$1"
  local package="$2"
  local repository="${registry_host}:${registry_port}/waycloak/${name}"
  local reference
  reference="$(
    KO_DOCKER_REPO="$repository" \
      go run github.com/google/ko@v0.19.1 build \
        --bare \
        --sbom=spdx \
        --platform=linux/amd64 \
        "$package"
  )"
  if [[ ! "$reference" =~ ^${repository}@sha256:[a-f0-9]{64}$ ]]; then
    printf 'unexpected ko reference for %s: %s\n' "$name" "$reference" >&2
    return 1
  fi
  printf '%s\n' "$reference"
}

controller_ref="$(publish_image replacement-controller ./cmd/replacement-controller)"
cni_ref="$(publish_image waycloak-cni ./cmd/waycloak-cni)"
node_agent_ref="$(publish_image waycloak-node-agent ./cmd/waycloak-node-agent)"
gateway_agent_ref="$(publish_image waycloak-gateway-agent ./cmd/waycloak-gateway-agent)"

mkdir -p "$work_dir/chart"
chart_version="$(awk '$1 == "version:" {print $2; exit}' charts/waycloak/Chart.yaml)"
if [[ ! "$chart_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][a-zA-Z0-9.]+)?$ ]]; then
  printf 'chart has an invalid version: %s\n' "$chart_version" >&2
  exit 1
fi
helm package charts/waycloak --destination "$work_dir/chart" >/dev/null
helm push "$work_dir/chart/waycloak-${chart_version}.tgz" \
  "oci://${registry_host}:${registry_port}/charts" >"$work_dir/chart-push.txt"
chart_digest="$(awk '$1 == "Digest:" {print $2}' "$work_dir/chart-push.txt")"
if [[ ! "$chart_digest" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  printf 'Helm did not report an exact chart digest\n' >&2
  exit 1
fi
chart_ref="oci://${registry_host}:${registry_port}/charts/waycloak@${chart_digest}"

go run ./hack/corerelease \
  --version "$release_version" \
  --chart "$chart_ref" \
  --image "replacement-controller=$controller_ref" \
  --image "waycloak-cni=$cni_ref" \
  --image "waycloak-node-agent=$node_agent_ref" \
  --image "waycloak-gateway-agent=$gateway_agent_ref" \
  --image "gluetun=$gluetun_ref" \
  --image "pause=$pause_ref" \
  >"$work_dir/release-manifest.json"

CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X main.version=${release_version}" \
  -o "$work_dir/waycloakctl" ./cmd/waycloakctl

"$work_dir/waycloakctl" preflight --output json >"$work_dir/preflight.json"
jq -e '.compatible == true and .profile == "networking.waycloak.io/Core-v1"' \
  "$work_dir/preflight.json" >/dev/null

"$work_dir/waycloakctl" install plan \
  --release-manifest "$work_dir/release-manifest.json" \
  --namespace "$system_namespace" \
  --release "$release_name" \
  --output json \
  >"$work_dir/install-plan.json"
plan_id="$(jq -r '.planID' "$work_dir/install-plan.json")"
if [[ ! "$plan_id" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  printf 'install plan lacks an exact identity\n' >&2
  exit 1
fi
if grep -Eqi 'password|privateKey|username|latest' "$work_dir/install-plan.json"; then
  printf 'install plan contains a forbidden mutable or credential field\n' >&2
  exit 1
fi

if "$work_dir/waycloakctl" install apply \
  --plan "$work_dir/install-plan.json" \
  --confirm sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff; then
  printf 'wrong install confirmation unexpectedly succeeded\n' >&2
  exit 1
fi
if kubectl get namespace "$system_namespace" >/dev/null 2>&1; then
  printf 'refused install mutated the cluster\n' >&2
  exit 1
fi

apply_started="$(date +%s)"
"$work_dir/waycloakctl" install apply \
  --plan "$work_dir/install-plan.json" \
  --confirm "$plan_id"
apply_elapsed="$(( $(date +%s) - apply_started ))"
if (( apply_elapsed >= 600 )); then
  printf 'exact-artifact install apply exceeded 10 minutes: %ss\n' "$apply_elapsed" >&2
  exit 1
fi

kubectl rollout status deployment/waycloak-controller \
  --namespace "$system_namespace" --timeout=2m
kubectl rollout status daemonset/waycloak-cni-installer \
  --namespace "$system_namespace" --timeout=2m
kubectl rollout status daemonset/waycloak-node-agent \
  --namespace "$system_namespace" --timeout=2m
kubectl wait node --all \
  --for=jsonpath='{.metadata.labels.networking\.waycloak\.io\.node-restriction\.kubernetes\.io/core-ready}'=true \
  --timeout=2m

manifest_digest="$(jq -r '.manifestDigest' "$work_dir/release-manifest.json")"
test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.spec.releaseIdentity.version}')" = "$release_version"
test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.spec.releaseIdentity.manifestDigest}')" = "$manifest_digest"
test "$(kubectl get deployment waycloak-controller -n "$system_namespace" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$controller_ref"
test "$(kubectl get daemonset waycloak-cni-installer -n "$system_namespace" -o jsonpath='{.spec.template.spec.initContainers[0].image}')" = "$cni_ref"
test "$(kubectl get daemonset waycloak-node-agent -n "$system_namespace" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$node_agent_ref"

node="$(kind get nodes --name "$cluster_name" | head -n1)"
docker exec "$node" test -x /opt/cni/bin/waycloak-cni
docker exec "$node" test -f /var/lib/cni/waycloak/install-receipt.json
docker exec "$node" grep -q '"type": "waycloak-cni"' /etc/cni/net.d/10-kindnet.conflist
docker exec "$node" cat /var/lib/cni/waycloak/install-receipt.json \
  | jq -e --arg version "$release_version" --arg digest "$manifest_digest" \
      '.releaseIdentity.version == $version and .releaseIdentity.manifestDigest == $digest' >/dev/null

doctor_deadline="$((SECONDS + 120))"
until "$work_dir/waycloakctl" doctor --output json \
  >"$work_dir/doctor.json" 2>"$work_dir/doctor-error.txt"; do
  if (( SECONDS >= doctor_deadline )); then
    cat "$work_dir/doctor.json" >&2
    cat "$work_dir/doctor-error.txt" >&2
    kubectl get vpngatewayclass gluetun.waycloak.io -o yaml >&2
    exit 1
  fi
  sleep 2
done
jq -e '.healthy == true and .nodeCapabilityStates.CNICapable == 1' \
  "$work_dir/doctor.json" >/dev/null

printf 'exact-artifact Kind install apply completed in %ss\n' "$apply_elapsed"
