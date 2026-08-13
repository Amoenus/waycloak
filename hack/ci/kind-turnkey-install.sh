#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -Eeuo pipefail

trap 'printf "turnkey failure at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR

readonly cluster_name="waycloak-turnkey-ci"
readonly registry_name="waycloak-turnkey-registry"
readonly registry_port="5001"
readonly registry_host="waycloak-registry.invalid"
readonly registry_image="registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
readonly kind_node_image="kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5"
readonly pause_ref="registry.k8s.io/pause@sha256:278fb9dbcca9518083ad1e11276933a2e96f23de604a3a08cc3c80002767d24c"
readonly curl_ref="docker.io/curlimages/curl:8.14.1@sha256:9a1ed35addb45476afa911696297f8e115993df459278ed036182dd2cd22b67b"
readonly release_version="v0.0.0-turnkey-ci"
readonly baseline_release_version="v0.0.0-turnkey-ci-baseline"
readonly port_forward_release_version="v0.0.0-turnkey-ci-port-forward"
readonly system_namespace="waycloak-system"
readonly release_name="waycloak"
readonly smoke_namespace="waycloak-smoke"

work_dir="$(mktemp -d)"
metrics_forward_pid=""

cleanup() {
  status="$?"
  if [[ -n "$metrics_forward_pid" ]]; then
    kill "$metrics_forward_pid" >/dev/null 2>&1 || true
    wait "$metrics_forward_pid" >/dev/null 2>&1 || true
  fi
  if (( status != 0 )) && kind get clusters 2>/dev/null | grep -qx "$cluster_name"; then
    if [[ -s "$work_dir/verify.json" ]]; then
      printf '%s\n' '--- waycloakctl verify report ---' >&2
      cat "$work_dir/verify.json" >&2
    fi
    kubectl get pods --all-namespaces -o wide >&2 || true
    kubectl get events --all-namespaces --sort-by=.lastTimestamp >&2 || true
    kubectl describe deployment/waycloak-controller \
      --namespace "$system_namespace" >&2 || true
    kubectl describe daemonset/waycloak-cni-installer \
      --namespace "$system_namespace" >&2 || true
    kubectl describe daemonset/waycloak-node-agent \
      --namespace "$system_namespace" >&2 || true
    kubectl logs --namespace "$system_namespace" \
      --selector app.kubernetes.io/instance="$release_name" \
      --all-containers --prefix --tail=200 >&2 || true
    kubectl get vpngateway/disposable --namespace "$smoke_namespace" \
      -o yaml >&2 || true
    while IFS= read -r pod; do
      [[ -n "$pod" ]] || continue
      kubectl describe --namespace "$smoke_namespace" "$pod" >&2 || true
      containers="$(kubectl get --namespace "$smoke_namespace" "$pod" \
        -o jsonpath='{.spec.containers[*].name}' 2>/dev/null || true)"
      for container in $containers; do
        kubectl logs --namespace "$smoke_namespace" "$pod" \
          --container "$container" --prefix --tail=200 >&2 || true
        kubectl logs --namespace "$smoke_namespace" "$pod" \
          --container "$container" --prefix --previous --tail=200 >&2 || true
      done
    done < <(kubectl get pods --namespace "$smoke_namespace" -o name 2>/dev/null)
  fi
  kind delete cluster --name "$cluster_name" >/dev/null 2>&1 || true
  docker rm --force "$registry_name" >/dev/null 2>&1 || true
  rm -rf -- "$work_dir"
}
trap cleanup EXIT

capture_metrics() {
  local output_path="$1"
  local metrics_deadline

  kubectl port-forward --namespace "$system_namespace" \
    service/waycloak-controller 18080:8080 \
    >"$work_dir/metrics-port-forward.log" 2>&1 &
  metrics_forward_pid="$!"
  metrics_deadline="$((SECONDS + 30))"
  until curl --fail --silent --show-error \
    http://127.0.0.1:18080/metrics >"$output_path"; do
    if ! kill -0 "$metrics_forward_pid" >/dev/null 2>&1 || (( SECONDS >= metrics_deadline )); then
      cat "$work_dir/metrics-port-forward.log" >&2
      return 1
    fi
    sleep 1
  done
  kill "$metrics_forward_pid"
  wait "$metrics_forward_pid" >/dev/null 2>&1 || true
  metrics_forward_pid=""
}

wait_for_agent_socket() {
  local node_name="$1"
  local deadline="$((SECONDS + 30))"
  until docker exec "$node_name" test -S /run/waycloak/cni-agent.sock && \
    docker exec "$node_name" test -f /run/waycloak/cni-auth.key; do
    if (( SECONDS >= deadline )); then
      printf 'node agent did not recreate its authenticated local socket within 30s\n' >&2
      return 1
    fi
    sleep 1
  done
}

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
  local image_label="${3:-}"
  local repository="${registry_host}:${registry_port}/waycloak/${name}"
  local -a label_args=()
  local reference
  if [[ -n "$image_label" ]]; then
    label_args=(--image-label "$image_label")
  fi
  reference="$(
    KO_DOCKER_REPO="$repository" \
      go run github.com/google/ko@v0.19.1 build \
        --bare \
        --sbom=spdx \
        --platform=linux/amd64 \
        "${label_args[@]}" \
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
gateway_runtime_ref="$(publish_image waycloak-gateway-runtime ./cmd/waycloak-gateway-runtime)"
qbittorrent_adapter_ref="$(publish_image waycloak-qbittorrent-adapter ./cmd/waycloak-qbittorrent-adapter)"
gluetun_ref="$(publish_image fake-gluetun ./test/fixtures/fake-gluetun)"
baseline_controller_ref="$(publish_image replacement-controller-baseline ./cmd/replacement-controller io.waycloak.lifecycle=baseline)"
baseline_cni_ref="$(publish_image waycloak-cni-baseline ./cmd/waycloak-cni io.waycloak.lifecycle=baseline)"
baseline_node_agent_ref="$(publish_image waycloak-node-agent-baseline ./cmd/waycloak-node-agent io.waycloak.lifecycle=baseline)"
observer_ref="$(publish_image egress-observer ./test/fixtures/egress-observer)"
probe_ref="$(publish_image waycloak-probe ./cmd/waycloak-probe)"

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
# The install transaction treats the authoring-only KCL identity as opaque.
# Reuse a resolvable exact fixture artifact here; the release workflow separately
# proves KCL packaging, OCI media, signature, SBOM, provenance, and rendering.
kcl_ref="$chart_ref"

readonly baseline_chart_version="0.0.0-lifecycle-baseline"
helm package charts/waycloak \
  --version "$baseline_chart_version" \
  --app-version "$baseline_release_version" \
  --destination "$work_dir/chart" >/dev/null
helm push "$work_dir/chart/waycloak-${baseline_chart_version}.tgz" \
  "oci://${registry_host}:${registry_port}/charts" >"$work_dir/baseline-chart-push.txt"
baseline_chart_digest="$(awk '$1 == "Digest:" {print $2}' "$work_dir/baseline-chart-push.txt")"
if [[ ! "$baseline_chart_digest" =~ ^sha256:[a-f0-9]{64}$ || "$baseline_chart_digest" == "$chart_digest" ]]; then
  printf 'Helm did not publish two distinct exact lifecycle chart identities\n' >&2
  exit 1
fi
baseline_chart_ref="oci://${registry_host}:${registry_port}/charts/waycloak@${baseline_chart_digest}"
baseline_kcl_ref="$baseline_chart_ref"

go run ./hack/release \
  --version "$release_version" \
  --chart "$chart_ref" \
  --kcl "$kcl_ref" \
  --image "replacement-controller=$controller_ref" \
  --image "waycloak-cni=$cni_ref" \
  --image "waycloak-node-agent=$node_agent_ref" \
  --image "waycloak-gateway-agent=$gateway_agent_ref" \
  --image "waycloak-gateway-runtime=$gateway_runtime_ref" \
  --image "waycloak-qbittorrent-adapter=$qbittorrent_adapter_ref" \
  --image "gluetun=$gluetun_ref" \
  --image "pause=$pause_ref" \
  >"$work_dir/release-manifest.json"

go run ./hack/release \
  --version "$baseline_release_version" \
  --chart "$baseline_chart_ref" \
  --kcl "$baseline_kcl_ref" \
  --image "replacement-controller=$baseline_controller_ref" \
  --image "waycloak-cni=$baseline_cni_ref" \
  --image "waycloak-node-agent=$baseline_node_agent_ref" \
  --image "waycloak-gateway-agent=$gateway_agent_ref" \
  --image "waycloak-gateway-runtime=$gateway_runtime_ref" \
  --image "waycloak-qbittorrent-adapter=$qbittorrent_adapter_ref" \
  --image "gluetun=$gluetun_ref" \
  --image "pause=$pause_ref" \
  >"$work_dir/baseline-release-manifest.json"

go run ./hack/release \
  --version "$port_forward_release_version" \
  --chart "$chart_ref" \
  --kcl "$kcl_ref" \
  --profile networking.waycloak.io/Core-v1 \
  --image "replacement-controller=$controller_ref" \
  --image "waycloak-cni=$cni_ref" \
  --image "waycloak-node-agent=$node_agent_ref" \
  --image "waycloak-gateway-agent=$gateway_agent_ref" \
  --image "waycloak-gateway-runtime=$gateway_runtime_ref" \
  --image "waycloak-qbittorrent-adapter=$qbittorrent_adapter_ref" \
  --image "gluetun=$gluetun_ref" \
  --image "pause=$pause_ref" \
  >"$work_dir/port-forward-release-manifest.json"

CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X main.version=${release_version}" \
  -o "$work_dir/waycloakctl" ./cmd/waycloakctl

preflight_status=0
"$work_dir/waycloakctl" preflight --output json >"$work_dir/preflight.json" || preflight_status="$?"
if [[ "$preflight_status" != 0 ]] || \
  ! jq -e '.compatible == true and .profile == "networking.waycloak.io/Core-v1"' \
  "$work_dir/preflight.json" >/dev/null; then
  cat "$work_dir/preflight.json" >&2
  printf 'turnkey preflight did not accept the pinned support row\n' >&2
  exit 1
fi

"$work_dir/waycloakctl" install plan \
  --release-manifest "$work_dir/baseline-release-manifest.json" \
  --namespace "$system_namespace" \
  --release "$release_name" \
  --output json \
  >"$work_dir/install-plan.json"
plan_id="$(jq -r '.planID' "$work_dir/install-plan.json")"
if [[ ! "$plan_id" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  printf 'install plan lacks an exact identity\n' >&2
  exit 1
fi
jq -e '
  .operation == "CleanInstall" and
  .source.state == "Absent" and
  (.targetCRDIdentities | length) == 6 and
  (.preflightDigest | test("^sha256:[a-f0-9]{64}$")) and
  .nodeArchitecture == "amd64" and
  .metadata.nodeArchitecture == "amd64" and
  (.valuesYAML | contains("kubernetes.io/arch:")) and
  (.valuesYAML | contains("serviceIP: \"10.96.0.10\"")) and
  (.valuesYAML | contains("domain: \"cluster.local\""))
' "$work_dir/install-plan.json" >/dev/null
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
  --for=jsonpath='{.metadata.labels.networking\.waycloak\.io\.node-restriction\.kubernetes\.io/cni-ready}'=true \
  --timeout=2m

manifest_digest="$(jq -r '.manifestDigest' "$work_dir/baseline-release-manifest.json")"
test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.spec.releaseIdentity.version}')" = "$baseline_release_version"
test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.spec.releaseIdentity.manifestDigest}')" = "$manifest_digest"
test "$(kubectl get deployment waycloak-controller -n "$system_namespace" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$baseline_controller_ref"
test "$(kubectl get daemonset waycloak-cni-installer -n "$system_namespace" -o jsonpath='{.spec.template.spec.initContainers[0].image}')" = "$baseline_cni_ref"
test "$(kubectl get daemonset waycloak-node-agent -n "$system_namespace" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$baseline_node_agent_ref"
test "$(kubectl get daemonset waycloak-cni-installer -n "$system_namespace" -o jsonpath='{.spec.template.spec.nodeSelector.kubernetes\.io/arch}')" = amd64
test "$(kubectl get daemonset waycloak-cni-installer -n "$system_namespace" -o jsonpath='{.spec.template.spec.hostNetwork}')" = true
test "$(kubectl get daemonset waycloak-cni-installer -n "$system_namespace" -o jsonpath='{.spec.template.spec.dnsPolicy}')" = ClusterFirstWithHostNet
test "$(kubectl get daemonset waycloak-node-agent -n "$system_namespace" -o jsonpath='{.spec.template.spec.nodeSelector.kubernetes\.io/arch}')" = amd64

node="$(kind get nodes --name "$cluster_name" | head -n1)"
docker exec "$node" test -x /opt/cni/bin/waycloak-cni
docker exec "$node" test -f /var/lib/cni/waycloak/install-receipt.json
docker exec "$node" grep -q '"type": "waycloak-cni"' /etc/cni/net.d/10-kindnet.conflist
docker exec "$node" grep -q '"agentSocket": "/run/waycloak/cni-agent.sock"' /etc/cni/net.d/10-kindnet.conflist
docker exec "$node" grep -q '"agentKeyFile": "/run/waycloak/cni-auth.key"' /etc/cni/net.d/10-kindnet.conflist
docker exec "$node" test -S /run/waycloak/cni-agent.sock
docker exec "$node" test -f /run/waycloak/cni-auth.key
docker exec "$node" cat /var/lib/cni/waycloak/install-receipt.json \
  | jq -e --arg version "$baseline_release_version" --arg digest "$manifest_digest" \
      '.releaseIdentity.version == $version and .releaseIdentity.manifestDigest == $digest' >/dev/null

# The installer sandbox must not depend on the chained CNI or node-agent socket
# it is responsible for upgrading. Remove only the exact local socket, restart
# the installer, and prove it can still re-establish the exact receipt before
# restarting the agent to recover its authenticated local protocol.
docker exec "$node" rm -f /run/waycloak/cni-agent.sock
kubectl delete pod --namespace "$system_namespace" \
  --selector app.kubernetes.io/component=cni-installer --wait=true
kubectl rollout status daemonset/waycloak-cni-installer \
  --namespace "$system_namespace" --timeout=2m
docker exec "$node" cat /var/lib/cni/waycloak/install-receipt.json \
  | jq -e --arg version "$baseline_release_version" --arg digest "$manifest_digest" \
      '.releaseIdentity.version == $version and .releaseIdentity.manifestDigest == $digest' >/dev/null
kubectl delete pod --namespace "$system_namespace" \
  --selector app.kubernetes.io/component=node-agent --wait=true
kubectl rollout status daemonset/waycloak-node-agent \
  --namespace "$system_namespace" --timeout=2m
wait_for_agent_socket "$node"

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

test "$(kubectl get service waycloak-controller -n "$system_namespace" -o jsonpath='{.spec.ports[?(@.name=="metrics")].port}')" = 8080
capture_metrics "$work_dir/metrics-initial.txt"
for metric in \
  waycloak_resources \
  waycloak_resource_condition_objects \
  waycloak_enrolled_pods \
  waycloak_workload_allocations \
  waycloak_metrics_collection_success \
  controller_runtime_reconcile_errors_total; do
  grep -q "^${metric}" "$work_dir/metrics-initial.txt"
done
grep -q '^waycloak_resources{resource="vpngatewayclass"} 1$' "$work_dir/metrics-initial.txt"
test "$(grep -c '^waycloak_metrics_collection_success' "$work_dir/metrics-initial.txt")" -eq 8
if grep -q '^waycloak_metrics_collection_success.* 0$' "$work_dir/metrics-initial.txt"; then
  printf 'Waycloak initial metrics collection was incomplete\n' >&2
  exit 1
fi
if grep '^waycloak_' "$work_dir/metrics-initial.txt" \
  | grep -Eqi 'waycloak-system|gluetun\.waycloak\.io|sha256:|10\.[0-9]+\.[0-9]+\.[0-9]+'; then
  printf 'Waycloak aggregate metrics exposed an object or network identity\n' >&2
  exit 1
fi

readonly observer_ip="198.18.0.1"
readonly observer_port="8443"
docker exec "$node" ip address add "${observer_ip}/32" dev lo
docker exec "$node" iptables --table nat --insert POSTROUTING 1 \
  --destination "${observer_ip}/32" --jump ACCEPT

mkdir -p "$work_dir/observer-tls"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$work_dir/observer-tls/tls.key" \
  -out "$work_dir/observer-tls/tls.crt" \
  -subj "/CN=Waycloak disposable egress observer" \
  -addext "basicConstraints=critical,CA:FALSE" \
  -addext "keyUsage=critical,digitalSignature,keyEncipherment" \
  -addext "extendedKeyUsage=serverAuth" \
  -addext "subjectAltName=IP:${observer_ip}" >/dev/null 2>&1

mapfile -t gateway_keys < <(go run ./test/fixtures/fake-gluetun --mode=keygen)
mapfile -t exit_keys < <(go run ./test/fixtures/fake-gluetun --mode=keygen)
if (( ${#gateway_keys[@]} != 2 || ${#exit_keys[@]} != 2 )); then
  printf 'disposable WireGuard key generation failed\n' >&2
  exit 1
fi

kubectl create namespace "$smoke_namespace"
kubectl create secret tls observer-tls --namespace "$smoke_namespace" \
  --cert "$work_dir/observer-tls/tls.crt" \
  --key "$work_dir/observer-tls/tls.key"
kubectl create configmap observer-ca --namespace "$smoke_namespace" \
  --from-file=ca.crt="$work_dir/observer-tls/tls.crt"
kubectl create secret generic fake-gateway-credentials --namespace "$smoke_namespace" \
  --from-literal=username="${gateway_keys[0]}" \
  --from-literal=password="${exit_keys[1]}"
kubectl create secret generic fake-exit-keys --namespace "$smoke_namespace" \
  --from-literal=private-key="${exit_keys[0]}" \
  --from-literal=peer-public-key="${gateway_keys[1]}"

cat <<EOF | kubectl apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: waycloak-gateway-secret-reader
  namespace: ${smoke_namespace}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: waycloak-gateway-secret-reader
subjects:
  - kind: ServiceAccount
    name: ${release_name}
    namespace: ${system_namespace}
---
apiVersion: v1
kind: Service
metadata:
  name: fake-wireguard-exit
  namespace: ${smoke_namespace}
spec:
  selector:
    app: fake-wireguard-exit
  ports:
    - name: wireguard
      port: 51820
      protocol: UDP
      targetPort: 51820
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fake-wireguard-exit
  namespace: ${smoke_namespace}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: fake-wireguard-exit
  template:
    metadata:
      labels:
        app: fake-wireguard-exit
    spec:
      automountServiceAccountToken: false
      containers:
        - name: exit
          image: ${gluetun_ref}
          args: ["--mode=exit"]
          env:
            - name: WIREGUARD_PRIVATE_KEY
              valueFrom:
                secretKeyRef:
                  name: fake-exit-keys
                  key: private-key
            - name: WIREGUARD_PEER_PUBLIC_KEY
              valueFrom:
                secretKeyRef:
                  name: fake-exit-keys
                  key: peer-public-key
          ports:
            - name: wireguard
              containerPort: 51820
              protocol: UDP
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
              add: ["NET_ADMIN"]
            runAsUser: 0
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: egress-observer
  namespace: ${smoke_namespace}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: egress-observer
  template:
    metadata:
      labels:
        app: egress-observer
    spec:
      automountServiceAccountToken: false
      hostNetwork: true
      nodeName: ${node}
      containers:
        - name: observer
          image: ${observer_ref}
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
          volumeMounts:
            - name: tls
              mountPath: /tls
              readOnly: true
      volumes:
        - name: tls
          secret:
            secretName: observer-tls
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: fake-gluetun
  namespace: ${smoke_namespace}
data:
  FIREWALL_OUTBOUND_SUBNETS: "100.96.0.0/16"
  FAKE_DNS_A: "${observer_ip}"
  WIREGUARD_ENDPOINT: "fake-wireguard-exit.${smoke_namespace}.svc:51820"
  WIREGUARD_STARTUP_DELAY: "40s"
---
apiVersion: networking.waycloak.io/v1beta1
kind: VPNGateway
metadata:
  name: disposable
  namespace: ${smoke_namespace}
  labels:
    verify.waycloak.io/dedicated: "true"
spec:
  gatewayClassName: gluetun.waycloak.io
  nativeConfigRefs:
    - role: networking.waycloak.io/GluetunEnvironment
      name: fake-gluetun
  credentialRefs:
    - role: networking.waycloak.io/OpenVPNCredentials
      name: fake-gateway-credentials
  requestedFeatures:
    - networking.waycloak.io/TCP
    - networking.waycloak.io/DNSContainment
  allowedRoutes:
    namespaces:
      from: Same
  clusterTraffic:
    mode: TunnelAll
  dns:
    mode: Gateway
EOF

kubectl rollout status deployment/fake-wireguard-exit --namespace "$smoke_namespace" --timeout=2m
kubectl rollout status deployment/egress-observer --namespace "$smoke_namespace" --timeout=2m
kubectl wait vpngateway/disposable --namespace "$smoke_namespace" \
  --for=condition=Ready --timeout=3m

# Exercise the application-facing resolver contract with libcurl after the
# gateway's UDP/TCP A/AAAA/EDNS probes have made DNSReady current and true.
# The cluster-qualified lookup must remain on the reviewed Kubernetes DNS path;
# the external lookup must resolve only through the gateway engine and traverse
# the disposable tunnel to the observer.
cat <<EOF | kubectl apply -f -
apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: dns-contract
  namespace: ${smoke_namespace}
spec:
  parentRefs:
    - group: networking.waycloak.io
      kind: VPNGateway
      namespace: ${smoke_namespace}
      name: disposable
  requiredFeatures:
    - networking.waycloak.io/TCP
    - networking.waycloak.io/DNSContainment
---
apiVersion: v1
kind: Pod
metadata:
  name: curl-dns-contract
  namespace: ${smoke_namespace}
  labels:
    networking.waycloak.io/egress-route: dns-contract
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  containers:
    - name: curl
      image: ${curl_ref}
      command:
        - sh
        - -ec
        - |
          grep -Eq '^options .*ndots:1([[:space:]]|$)' /etc/resolv.conf
          curl -V | grep -q AsynchDNS
          curl -vk --connect-timeout 3 https://kubernetes.default.svc.cluster.local/version 2>&1 | grep -Eq 'Trying 10\.'
          curl -ksSf --connect-timeout 10 https://external.waycloak.test:${observer_port}/ip | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        runAsNonRoot: true
        runAsUser: 1000
        runAsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
EOF
kubectl wait vpnegressroute/dns-contract --namespace "$smoke_namespace" \
  --for=condition=Ready --timeout=2m
kubectl wait pod/curl-dns-contract --namespace "$smoke_namespace" \
  --for=jsonpath='{.status.phase}'=Succeeded --timeout=2m
test "$(kubectl get pod/curl-dns-contract --namespace "$smoke_namespace" \
  -o json | jq '[.spec.dnsConfig.options[]? | select(.name == "ndots" and .value == "1")] | length')" = "1"
kubectl delete pod/curl-dns-contract vpnegressroute/dns-contract \
  --namespace "$smoke_namespace" --wait=true

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: metrics-private-canary
  namespace: ${smoke_namespace}
  labels:
    networking.waycloak.io/egress-route: metrics-missing-route-private
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  containers:
    - name: must-not-start
      image: ${probe_ref}
      command: ["sh", "-c", "exit 97"]
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
EOF

metrics_canary_deadline="$((SECONDS + 60))"
until [[ -n "$(kubectl get pod/metrics-private-canary --namespace "$smoke_namespace" -o jsonpath='{.spec.nodeName}')" ]]; do
  if (( SECONDS >= metrics_canary_deadline )); then
    kubectl describe pod/metrics-private-canary --namespace "$smoke_namespace" >&2
    printf 'metrics privacy canary was not scheduled to the CNI-ready node\n' >&2
    exit 1
  fi
  sleep 1
done
metrics_canary_uid="$(kubectl get pod/metrics-private-canary --namespace "$smoke_namespace" -o jsonpath='{.metadata.uid}')"
until kubectl get events --namespace "$smoke_namespace" \
  --field-selector involvedObject.kind=Pod,involvedObject.name=metrics-private-canary -o json | \
  jq -e '.items[] | select(.reason == "FailedCreatePodSandBox") | .message | test("waycloak|egress route|binding"; "i")' >/dev/null; do
  if [[ "$(kubectl get pod/metrics-private-canary --namespace "$smoke_namespace" -o json | \
    jq '[.status.containerStatuses[]? | select(.state.running or .state.terminated or ((.containerID // "") | length > 0))] | length')" != "0" ]]; then
    printf 'metrics privacy canary application container started after failed enrollment resolution\n' >&2
    exit 1
  fi
  if (( SECONDS >= metrics_canary_deadline )); then
    kubectl describe pod/metrics-private-canary --namespace "$smoke_namespace" >&2
    printf 'metrics privacy canary did not produce a Waycloak sandbox failure\n' >&2
    exit 1
  fi
  sleep 1
done
capture_metrics "$work_dir/metrics-live.txt"
test "$(grep -c '^waycloak_metrics_collection_success' "$work_dir/metrics-live.txt")" -eq 8
if grep -q '^waycloak_metrics_collection_success.* 0$' "$work_dir/metrics-live.txt"; then
  printf 'Waycloak live metrics collection was incomplete\n' >&2
  exit 1
fi
for expected in \
  'waycloak_resources{resource="vpngateway"} 1' \
  'waycloak_resource_condition_objects{condition="Ready",current="true",reason="Ready",resource="vpngateway",status="True"} 1' \
  'waycloak_resource_condition_objects{condition="TunnelReady",current="true",reason="TunnelReady",resource="vpngateway",status="True"} 1' \
  'waycloak_resource_condition_objects{condition="DNSReady",current="true",reason="DNSReady",resource="vpngateway",status="True"} 1' \
  'waycloak_enrolled_pods{state="binding_absent"} 1'; do
  grep -Fqx "$expected" "$work_dir/metrics-live.txt"
done
for forbidden in \
  "$smoke_namespace" \
  metrics-private-canary \
  metrics-missing-route-private \
  "$metrics_canary_uid" \
  "$observer_ip" \
  100.96.0.0/16; do
  if grep '^waycloak_' "$work_dir/metrics-live.txt" | grep -Fq "$forbidden"; then
    printf 'Waycloak aggregate metrics exposed privacy canary value: %s\n' "$forbidden" >&2
    exit 1
  fi
done
kubectl delete pod/metrics-private-canary --namespace "$smoke_namespace" --wait=true

probe_url="https://${observer_ip}:${observer_port}/ip"

run_disruptive_verify() {
  local label="$1"
  local before_owned_pods after_owned_pods required_confirmation
  local before_gateway_pod_uid after_gateway_pod_uid
  local expected_gateway_engine_image expected_gateway_agent_image
  local expected_gateway_release_version expected_gateway_release_digest
  local expected_gateway_revision
  before_owned_pods="$(kubectl get pods --namespace "$smoke_namespace" \
    --selector app.kubernetes.io/managed-by=waycloakctl --no-headers 2>/dev/null | wc -l)"
  before_gateway_pod_uid="$(kubectl get pod waycloak-gateway-disposable-0 \
    --namespace "$smoke_namespace" -o jsonpath='{.metadata.uid}')"
  expected_gateway_engine_image="$(kubectl get statefulset waycloak-gateway-disposable \
    --namespace "$smoke_namespace" -o jsonpath='{.spec.template.spec.containers[?(@.name=="vpn-engine")].image}')"
  expected_gateway_agent_image="$(kubectl get statefulset waycloak-gateway-disposable \
    --namespace "$smoke_namespace" -o jsonpath='{.spec.template.spec.containers[?(@.name=="gateway-agent")].image}')"
  expected_gateway_release_version="$(kubectl get statefulset waycloak-gateway-disposable \
    --namespace "$smoke_namespace" -o jsonpath='{.spec.template.metadata.annotations.runtime\.networking\.waycloak\.io/release-version}')"
  expected_gateway_release_digest="$(kubectl get statefulset waycloak-gateway-disposable \
    --namespace "$smoke_namespace" -o jsonpath='{.spec.template.metadata.annotations.runtime\.networking\.waycloak\.io/release-manifest-digest}')"
  expected_gateway_revision="$(kubectl get statefulset waycloak-gateway-disposable \
    --namespace "$smoke_namespace" -o jsonpath='{.status.updateRevision}')"
  test -n "$expected_gateway_revision"
  test "$expected_gateway_release_version" = "$(kubectl get vpngatewayclass gluetun.waycloak.io \
    -o jsonpath='{.spec.releaseIdentity.version}')"
  test "$expected_gateway_release_digest" = "$(kubectl get vpngatewayclass gluetun.waycloak.io \
    -o jsonpath='{.spec.releaseIdentity.manifestDigest}')"
  if "$work_dir/waycloakctl" verify \
    --namespace "$smoke_namespace" \
    --gateway disposable \
    --probe-image "$probe_ref" \
    --probe-url "$probe_url" \
    --probe-ca-configmap observer-ca \
    --confirm sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff \
    --output json >"$work_dir/refused-verify-${label}.json" 2>"$work_dir/refused-verify-${label}-error.txt"; then
    printf '%s disruptive verification accepted the wrong confirmation\n' "$label" >&2
    return 1
  fi
  required_confirmation="$(grep -Eo 'sha256:[a-f0-9]{64}' \
    "$work_dir/refused-verify-${label}-error.txt" | tail -n1)"
  if [[ ! "$required_confirmation" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    cat "$work_dir/refused-verify-${label}-error.txt" >&2
    printf '%s refused verification did not report its exact identity\n' "$label" >&2
    return 1
  fi
  after_owned_pods="$(kubectl get pods --namespace "$smoke_namespace" \
    --selector app.kubernetes.io/managed-by=waycloakctl --no-headers 2>/dev/null | wc -l)"
  test "$before_owned_pods" = "$after_owned_pods"
  test "$(kubectl get vpnegressroutes --namespace "$smoke_namespace" \
    --selector app.kubernetes.io/managed-by=waycloakctl --no-headers 2>/dev/null | wc -l)" = "0"

  if ! "$work_dir/waycloakctl" verify \
    --namespace "$smoke_namespace" \
    --gateway disposable \
    --probe-image "$probe_ref" \
    --probe-url "$probe_url" \
    --probe-ca-configmap observer-ca \
    --confirm "$required_confirmation" \
    --output json >"$work_dir/verify-${label}.json"; then
    cp "$work_dir/verify-${label}.json" "$work_dir/verify.json"
    return 1
  fi
  cp "$work_dir/verify-${label}.json" "$work_dir/verify.json"
  jq -e '.verified == true and .distinctEgress == true and
    .protectedSucceeded == true and .ordinarySucceeded == true and
    .outageProtectedDenied == true and .recoveryBindingReady == true and
    .tunnelLossVerified == true and .cleanupComplete == true' \
    "$work_dir/verify-${label}.json" >/dev/null
  after_gateway_pod_uid="$(kubectl get pod waycloak-gateway-disposable-0 \
    --namespace "$smoke_namespace" -o jsonpath='{.metadata.uid}')"
  if [[ -z "$before_gateway_pod_uid" || -z "$after_gateway_pod_uid" || \
    "$before_gateway_pod_uid" == "$after_gateway_pod_uid" ]]; then
    printf '%s explicit activation did not replace the exact gateway Pod\n' "$label" >&2
    return 1
  fi
  kubectl get pod waycloak-gateway-disposable-0 --namespace "$smoke_namespace" -o json | \
    jq -e --arg engine "$expected_gateway_engine_image" --arg agent "$expected_gateway_agent_image" \
      --arg version "$expected_gateway_release_version" --arg digest "$expected_gateway_release_digest" \
      --arg revision "$expected_gateway_revision" '
      (.spec.containers[] | select(.name == "vpn-engine") | .image) == $engine and
      (.spec.containers[] | select(.name == "gateway-agent") | .image) == $agent and
      .metadata.annotations["runtime.networking.waycloak.io/release-version"] == $version and
      .metadata.annotations["runtime.networking.waycloak.io/release-manifest-digest"] == $digest and
      .metadata.labels["controller-revision-hash"] == $revision and
      ([.status.containerStatuses[] | select(.ready == true)] | length) == 2
    ' >/dev/null
  # The verifier observes current-generation Ready on the recovered UID-bound
  # binding. Its outage Pod never receives an application process, so it can
  # originate zero direct-egress packets; the separate privileged CNI row also
  # proves this with node packet capture.
  test "$(kubectl get vpnegressroutes --namespace "$smoke_namespace" \
    --selector app.kubernetes.io/managed-by=waycloakctl --no-headers 2>/dev/null | wc -l)" = "0"
}

apply_exact_transition() {
  local label="$1"
  local manifest_path="$2"
  local expected_version="$3"
  local plan_path="$work_dir/install-plan-${label}.json"
  local expected_digest current_version before_revision after_revision plan_id
  local before_ca before_tls after_ca after_tls before_class_uid after_class_uid receipt_ready
  local helm_wrapper_dir stage_marker interrupted_pid startup_denied pod_name doctor_degraded
  local source_secret_path staged_revision repair_plan_path repair_plan_id repair_marker
  local direct_values_path direct_log target_chart
  local before_controller_image before_cni_image before_agent_image
  local before_gateway_pod_uid before_gateway_pod_revision after_gateway_pod_uid
  local gateway_rollout_ready expected_gateway_revision
  local expected_gateway_engine_image expected_gateway_agent_image

  expected_digest="$(jq -r '.manifestDigest' "$manifest_path")"
  current_version="$(kubectl get vpngatewayclass gluetun.waycloak.io \
    -o jsonpath='{.spec.releaseIdentity.version}')"
  before_revision="$(kubectl get secrets --namespace "$system_namespace" \
    --selector owner=helm,name="$release_name",status=deployed -o json | \
    jq -r '.items | if length == 1 then .[0].metadata.labels.version else error("ambiguous deployed Helm revision") end')"
  before_ca="$(kubectl get secret "${release_name}-observation-ca" --namespace "$system_namespace" -o json | \
    jq -r '[.metadata.uid, .data["ca.crt"]] | join("|")')"
  before_tls="$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o json | \
    jq -r '[.metadata.uid, .data["ca.crt"], .data["tls.crt"]] | join("|")')"
  before_class_uid="$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.metadata.uid}')"
  before_gateway_pod_uid="$(kubectl get pod waycloak-gateway-disposable-0 \
    --namespace "$smoke_namespace" -o jsonpath='{.metadata.uid}')"
  before_gateway_pod_revision="$(kubectl get pod waycloak-gateway-disposable-0 \
    --namespace "$smoke_namespace" -o jsonpath='{.metadata.labels.controller-revision-hash}')"
  test -n "$before_gateway_pod_uid"
  test -n "$before_gateway_pod_revision"
  expected_gateway_engine_image="$(jq -r '.images.gluetun.repository + "@" + .images.gluetun.digest' "$manifest_path")"
  expected_gateway_agent_image="$(jq -r '.images["waycloak-gateway-agent"].repository + "@" + .images["waycloak-gateway-agent"].digest' "$manifest_path")"
  source_secret_path="$work_dir/source-helm-revision-${label}.json"
  kubectl get secret "sh.helm.release.v1.${release_name}.v${before_revision}" \
    --namespace "$system_namespace" -o json >"$source_secret_path"

  "$work_dir/waycloakctl" install plan \
    --release-manifest "$manifest_path" \
    --namespace "$system_namespace" \
    --release "$release_name" \
    --output json >"$plan_path"
  plan_id="$(jq -r '.planID' "$plan_path")"
  jq -e --arg source "$current_version" --arg target "$expected_version" --arg digest "$expected_digest" '
    .operation == "ExactReleaseTransition" and
    .source.state == "Deployed" and
    .source.version == $source and
    .targetRelease.version == $target and
    .targetRelease.manifestDigest == $digest and
    (.source.crdIdentities | length) == 6 and
    (.targetCRDIdentities | length) == 6
  ' "$plan_path" >/dev/null

  if "$work_dir/waycloakctl" install apply \
    --plan "$plan_path" \
    --confirm sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff; then
    printf '%s transition accepted the wrong confirmation\n' "$label" >&2
    return 1
  fi
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io \
    -o jsonpath='{.spec.releaseIdentity.version}')" = "$current_version"
  test "$(kubectl get secrets --namespace "$system_namespace" \
    --selector owner=helm,name="$release_name",status=deployed -o json | \
    jq -r '.items[0].metadata.labels.version')" = "$before_revision"

  # Raw Helm has neither the reviewed source UID nor the immutable transition
  # journal. A changed release must fail during connected rendering, before it
  # can create a mixed controller/CNI/agent runtime. Exercise both forward and
  # rollback directions; apply_exact_transition is called once for each.
  before_controller_image="$(kubectl get deployment waycloak-controller \
    --namespace "$system_namespace" -o jsonpath='{.spec.template.spec.containers[0].image}')"
  before_cni_image="$(kubectl get daemonset waycloak-cni-installer \
    --namespace "$system_namespace" -o jsonpath='{.spec.template.spec.initContainers[0].image}')"
  before_agent_image="$(kubectl get daemonset waycloak-node-agent \
    --namespace "$system_namespace" -o jsonpath='{.spec.template.spec.containers[0].image}')"
  direct_values_path="$work_dir/direct-values-${label}.yaml"
  direct_log="$work_dir/direct-helm-${label}.log"
  target_chart="$(jq -r '.chart.repository + "@" + .chart.digest' "$plan_path")"
  jq -r '.valuesYAML' "$plan_path" >"$direct_values_path"
  if helm upgrade --install "$release_name" "$target_chart" \
    --namespace "$system_namespace" --server-side=true --force-conflicts \
    --values "$direct_values_path" >"$direct_log" 2>&1; then
    printf '%s raw Helm transition bypassed the immutable-class lifecycle\n' "$label" >&2
    return 1
  fi
  grep -q 'use waycloakctl install plan/apply for a journal-bound transition' "$direct_log"
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.metadata.uid}')" = "$before_class_uid"
  test "$(kubectl get deployment waycloak-controller --namespace "$system_namespace" \
    -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$before_controller_image"
  test "$(kubectl get daemonset waycloak-cni-installer --namespace "$system_namespace" \
    -o jsonpath='{.spec.template.spec.initContainers[0].image}')" = "$before_cni_image"
  test "$(kubectl get daemonset waycloak-node-agent --namespace "$system_namespace" \
    -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$before_agent_image"
  test "$(kubectl get secrets --namespace "$system_namespace" \
    --selector owner=helm,name="$release_name",status=deployed -o json | \
    jq -r '.items[0].metadata.labels.version')" = "$before_revision"
  kubectl wait vpnegressroute/transition-guard --namespace "$smoke_namespace" \
    --for=condition=Ready --timeout=10s

  helm_wrapper_dir="$work_dir/helm-wrapper-${label}"
  stage_marker="$work_dir/stage-complete-${label}"
  mkdir -p "$helm_wrapper_dir"
  cat >"$helm_wrapper_dir/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ " $* " == *"node-agent-transition-hold.yaml"* ]]; then
  "$WAYCLOAK_REAL_HELM" "$@"
  : >"$WAYCLOAK_STAGE_MARKER"
  while true; do sleep 1; done
fi
exec "$WAYCLOAK_REAL_HELM" "$@"
EOF
  chmod 0755 "$helm_wrapper_dir/helm"
  setsid env \
    PATH="$helm_wrapper_dir:$PATH" \
    WAYCLOAK_REAL_HELM="$(command -v helm)" \
    WAYCLOAK_STAGE_MARKER="$stage_marker" \
    "$work_dir/waycloakctl" install apply --plan "$plan_path" --confirm "$plan_id" \
    >"$work_dir/interrupted-${label}.log" 2>&1 &
  interrupted_pid="$!"
  for _ in $(seq 1 600); do
    if [[ -f "$stage_marker" ]]; then
      break
    fi
    if ! kill -0 "$interrupted_pid" 2>/dev/null; then
      wait "$interrupted_pid" || true
      cat "$work_dir/interrupted-${label}.log" >&2
      printf '%s transition exited before its staged interruption checkpoint\n' "$label" >&2
      return 1
    fi
    sleep 1
  done
  if [[ ! -f "$stage_marker" ]]; then
    kill -TERM -- "-$interrupted_pid" 2>/dev/null || true
    wait "$interrupted_pid" || true
    printf '%s transition did not reach its staged interruption checkpoint\n' "$label" >&2
    return 1
  fi
  kill -TERM -- "-$interrupted_pid"
  if wait "$interrupted_pid"; then
    printf '%s interrupted transition unexpectedly returned success\n' "$label" >&2
    return 1
  fi

  kubectl get configmap "${release_name}-release-transition" \
    --namespace "$system_namespace" -o json | \
    jq -e --arg release "$release_name" --arg plan "$plan_id" '
      .immutable == true and
      .metadata.annotations["install.waycloak.io/release"] == $release and
      .metadata.annotations["install.waycloak.io/transition-plan-id"] == $plan and
      (.data["plan.json"] | fromjson | .planID) == $plan
    ' >/dev/null
  "$work_dir/waycloakctl" install plan \
    --release-manifest "$manifest_path" \
    --namespace "$system_namespace" \
    --release "$release_name" \
    --output json >"$work_dir/recovered-plan-${label}.json"
  test "$(jq -S . "$plan_path")" = "$(jq -S . "$work_dir/recovered-plan-${label}.json")"

  doctor_degraded=false
  for _ in $(seq 1 60); do
    if ! "$work_dir/waycloakctl" doctor --output json \
      >"$work_dir/doctor-interrupted-${label}.json" 2>/dev/null && \
      jq -e '.healthy == false and (.problems | length) > 0' \
        "$work_dir/doctor-interrupted-${label}.json" >/dev/null; then
      doctor_degraded=true
      break
    fi
    sleep 1
  done
  if [[ "$doctor_degraded" != true ]]; then
    cat "$work_dir/doctor-interrupted-${label}.json" >&2 || true
    printf '%s interrupted transition retained stale healthy state\n' "$label" >&2
    return 1
  fi

  pod_name="${label}-interrupted-probe"
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
  namespace: ${smoke_namespace}
  labels:
    networking.waycloak.io/egress-route: transition-guard
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  containers:
    - name: probe
      image: ${probe_ref}
      env:
        - name: PROBE_URL
          value: ${probe_url}
        - name: PROBE_CA_FILE
          value: /observer-ca/ca.crt
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      volumeMounts:
        - name: observer-ca
          mountPath: /observer-ca
          readOnly: true
  volumes:
    - name: observer-ca
      configMap:
        name: observer-ca
EOF
  startup_denied=false
  for _ in $(seq 1 45); do
    if [[ "$(kubectl get pod "$pod_name" --namespace "$smoke_namespace" -o json | \
      jq '[.status.containerStatuses[]? | select(.state.running or .state.terminated or ((.containerID // "") | length > 0))] | length')" != "0" ]]; then
      printf '%s application container started during an interrupted release transition\n' "$label" >&2
      return 1
    fi
    if kubectl get events --namespace "$smoke_namespace" \
      --field-selector "involvedObject.kind=Pod,involvedObject.name=${pod_name}" -o json | \
      jq -e '.items[] | select(.reason == "FailedCreatePodSandBox" or .reason == "FailedScheduling")' >/dev/null; then
      startup_denied=true
      break
    fi
    sleep 1
  done
  if [[ "$startup_denied" != true ]]; then
    kubectl describe pod "$pod_name" --namespace "$smoke_namespace" >&2 || true
    printf '%s did not produce observable fail-closed startup denial\n' "$label" >&2
    return 1
  fi
  kubectl get pod "$pod_name" --namespace "$smoke_namespace" -o json | \
    jq -e '((.status.podIP // "") | length) == 0 and
      ([.status.containerStatuses[]? | select(.state.running or .state.terminated or ((.containerID // "") | length > 0))] | length) == 0' >/dev/null
  kubectl delete pod "$pod_name" --namespace "$smoke_namespace" --wait=true --timeout=2m

  if [[ "$label" == forward ]]; then
    staged_revision="$(kubectl get secrets --namespace "$system_namespace" \
      --selector owner=helm,name="$release_name",status=deployed -o json | \
      jq -r --argjson source "$before_revision" \
        '.items | map(select((.metadata.labels.version | tonumber) > $source)) |
         if length == 1 then .[0].metadata.labels.version else error("ambiguous staged Helm revision") end')"
    jq '{metadata: {labels: .metadata.labels, annotations: .metadata.annotations}, data: .data, type: .type}' \
      "$source_secret_path" | kubectl patch secret "sh.helm.release.v1.${release_name}.v${before_revision}" \
      --namespace "$system_namespace" --type merge --patch-file /dev/stdin
    kubectl patch secret "sh.helm.release.v1.${release_name}.v${staged_revision}" \
      --namespace "$system_namespace" --type merge \
      --patch '{"metadata":{"labels":{"status":"pending-upgrade"}},"data":{"release":"b3BhcXVlLWNvcnJ1cHQtcmVsZWFzZS1yZWNvcmQ="}}'

    repair_plan_path="$work_dir/install-repair-${label}.json"
    "$work_dir/waycloakctl" install repair plan \
      --namespace "$system_namespace" --release "$release_name" --output json >"$repair_plan_path"
    repair_plan_id="$(jq -r '.planID' "$repair_plan_path")"
    jq -e --arg source "$before_revision" --arg stuck "$staged_revision" '
      .kind == "InstallRepairPlan" and
      .repairSequence == "ExactHelmTransitionRepair-v1" and
      .checkpoint == "Staged" and
      .sourceRevision.version == ($source | tonumber) and
      .sourceRevision.status == "deployed" and
      .stuckRevision.version == ($stuck | tonumber) and
      .stuckRevision.status == "pending-upgrade" and
      (.sourceRevision.objectDigest | test("^sha256:[a-f0-9]{64}$")) and
      (.stuckRevision.objectDigest | test("^sha256:[a-f0-9]{64}$"))
    ' "$repair_plan_path" >/dev/null
    if grep -Eq 'opaque-corrupt-release-record|b3BhcXVlLWNvcnJ1cHQtcmVsZWFzZS1yZWNvcmQ=' "$repair_plan_path"; then
      printf 'Helm repair plan copied opaque release payload\n' >&2
      return 1
    fi
    if "$work_dir/waycloakctl" install repair apply --plan "$repair_plan_path" \
      --confirm sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff; then
      printf 'Helm repair accepted the wrong confirmation\n' >&2
      return 1
    fi
    kubectl get secret "sh.helm.release.v1.${release_name}.v${staged_revision}" \
      --namespace "$system_namespace" >/dev/null

    repair_marker="$work_dir/repair-candidate-deleted-${label}"
    cat >"$helm_wrapper_dir/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ " $* " == *" upgrade "* ]]; then
  : >"$WAYCLOAK_REPAIR_MARKER"
  while true; do sleep 1; done
fi
exec "$WAYCLOAK_REAL_HELM" "$@"
EOF
    chmod 0755 "$helm_wrapper_dir/helm"
    setsid env PATH="$helm_wrapper_dir:$PATH" \
      WAYCLOAK_REAL_HELM="$(command -v helm)" WAYCLOAK_REPAIR_MARKER="$repair_marker" \
      "$work_dir/waycloakctl" install repair apply --plan "$repair_plan_path" --confirm "$repair_plan_id" \
      >"$work_dir/interrupted-repair-${label}.log" 2>&1 &
    interrupted_pid="$!"
    for _ in $(seq 1 120); do
      [[ -f "$repair_marker" ]] && break
      if ! kill -0 "$interrupted_pid" 2>/dev/null; then
        wait "$interrupted_pid" || true
        cat "$work_dir/interrupted-repair-${label}.log" >&2
        printf 'Helm repair exited before its post-deletion interruption\n' >&2
        return 1
      fi
      sleep 1
    done
    [[ -f "$repair_marker" ]]
    kill -TERM -- "-$interrupted_pid"
    wait "$interrupted_pid" || true
    if kubectl get secret "sh.helm.release.v1.${release_name}.v${staged_revision}" \
      --namespace "$system_namespace" >/dev/null 2>&1; then
      printf 'interrupted Helm repair retained the exact stuck revision\n' >&2
      return 1
    fi
    kubectl get configmap "${release_name}-release-repair" --namespace "$system_namespace" -o json | \
      jq -e --arg plan "$repair_plan_id" '
        .immutable == true and
        .metadata.annotations["install.waycloak.io/repair-plan-id"] == $plan and
        (.data["repair.json"] | fromjson | .planID) == $plan
      ' >/dev/null
    "$work_dir/waycloakctl" install repair plan --namespace "$system_namespace" \
      --release "$release_name" --output json >"$work_dir/recovered-repair-${label}.json"
    test "$(jq -S . "$repair_plan_path")" = "$(jq -S . "$work_dir/recovered-repair-${label}.json")"
    if "$work_dir/waycloakctl" install plan --release-manifest "$manifest_path" \
      --namespace "$system_namespace" --release "$release_name" --output json >/dev/null; then
      printf 'ordinary install planning overlapped an active Helm repair\n' >&2
      return 1
    fi
    "$work_dir/waycloakctl" install repair apply \
      --plan "$work_dir/recovered-repair-${label}.json" --confirm "$repair_plan_id"
    if kubectl get configmap "${release_name}-release-repair" \
      --namespace "$system_namespace" >/dev/null 2>&1; then
      printf 'completed Helm repair retained its immutable journal\n' >&2
      return 1
    fi
  else
    "$work_dir/waycloakctl" install apply \
      --plan "$work_dir/recovered-plan-${label}.json" \
      --confirm "$plan_id"
  fi
  if kubectl get configmap "${release_name}-release-transition" \
    --namespace "$system_namespace" >/dev/null 2>&1; then
    printf '%s completed transition retained its lifecycle journal\n' "$label" >&2
    return 1
  fi
  kubectl rollout status deployment/waycloak-controller \
    --namespace "$system_namespace" --timeout=2m
  kubectl rollout status daemonset/waycloak-cni-installer \
    --namespace "$system_namespace" --timeout=2m
  kubectl rollout status daemonset/waycloak-node-agent \
    --namespace "$system_namespace" --timeout=2m

  after_revision="$(kubectl get secrets --namespace "$system_namespace" \
    --selector owner=helm,name="$release_name",status=deployed -o json | \
    jq -r '.items | if length == 1 then .[0].metadata.labels.version else error("ambiguous deployed Helm revision") end')"
  if (( after_revision <= before_revision )); then
    printf '%s transition did not advance the Helm revision\n' "$label" >&2
    return 1
  fi
  after_ca="$(kubectl get secret "${release_name}-observation-ca" --namespace "$system_namespace" -o json | \
    jq -r '[.metadata.uid, .data["ca.crt"]] | join("|")')"
  after_tls="$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o json | \
    jq -r '[.metadata.uid, .data["ca.crt"], .data["tls.crt"]] | join("|")')"
  after_class_uid="$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.metadata.uid}')"
  test "$after_ca" = "$before_ca"
  test "$after_tls" = "$before_tls"
  test "$after_class_uid" != "$before_class_uid"
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io \
    -o jsonpath='{.spec.releaseIdentity.version}')" = "$expected_version"
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io \
    -o jsonpath='{.spec.releaseIdentity.manifestDigest}')" = "$expected_digest"
  gateway_rollout_ready=false
  for _ in $(seq 1 60); do
    expected_gateway_revision="$(kubectl get statefulset waycloak-gateway-disposable \
      --namespace "$smoke_namespace" -o jsonpath='{.status.updateRevision}')"
    if [[ -n "$expected_gateway_revision" && "$expected_gateway_revision" != "$before_gateway_pod_revision" ]] && \
      kubectl get statefulset waycloak-gateway-disposable --namespace "$smoke_namespace" -o json | \
      jq -e --arg engine "$expected_gateway_engine_image" --arg agent "$expected_gateway_agent_image" \
        --arg version "$expected_version" --arg digest "$expected_digest" --arg revision "$expected_gateway_revision" '
        .spec.updateStrategy.type == "OnDelete" and
        (.spec.template.spec.containers[] | select(.name == "vpn-engine") | .image) == $engine and
        (.spec.template.spec.containers[] | select(.name == "gateway-agent") | .image) == $agent and
        .spec.template.metadata.annotations["runtime.networking.waycloak.io/release-version"] == $version and
        .spec.template.metadata.annotations["runtime.networking.waycloak.io/release-manifest-digest"] == $digest and
        .status.updateRevision == $revision
      ' >/dev/null && \
      kubectl get pod waycloak-gateway-disposable-0 --namespace "$smoke_namespace" -o json | \
      jq -e --arg engine "$expected_gateway_engine_image" --arg agent "$expected_gateway_agent_image" \
        --arg version "$expected_version" --arg digest "$expected_digest" --arg revision "$expected_gateway_revision" '
        (.spec.containers[] | select(.name == "vpn-engine") | .image) == $engine and
        (.spec.containers[] | select(.name == "gateway-agent") | .image) == $agent and
        .metadata.annotations["runtime.networking.waycloak.io/release-version"] == $version and
        .metadata.annotations["runtime.networking.waycloak.io/release-manifest-digest"] == $digest and
        .metadata.labels["controller-revision-hash"] == $revision and
        ([.status.containerStatuses[] | select(.ready == true)] | length) == 2
      ' >/dev/null; then
      gateway_rollout_ready=true
      break
    fi
    sleep 1
  done
  if [[ "$gateway_rollout_ready" != true ]]; then
    printf '%s transition did not activate its distinct exact gateway revision\n' "$label" >&2
    kubectl get statefulset waycloak-gateway-disposable --namespace "$smoke_namespace" -o yaml >&2
    return 1
  fi
  after_gateway_pod_uid="$(kubectl get pod waycloak-gateway-disposable-0 \
    --namespace "$smoke_namespace" -o jsonpath='{.metadata.uid}')"
  if [[ -z "$after_gateway_pod_uid" || "$after_gateway_pod_uid" == "$before_gateway_pod_uid" ]]; then
    printf '%s transition did not replace the exact stale gateway Pod\n' "$label" >&2
    return 1
  fi
  kubectl get deployment waycloak-controller --namespace "$system_namespace" -o json | \
    jq -e --arg version "$expected_version" --arg digest "$expected_digest" '
      .spec.template.spec.containers[] | select(.name == "controller") |
      (.args | index("--release-version=" + $version) != null) and
      (.args | index("--release-manifest-digest=" + $digest) != null)
    ' >/dev/null
  kubectl get daemonset waycloak-node-agent --namespace "$system_namespace" -o json | \
    jq -e --arg version "$expected_version" --arg digest "$expected_digest" '
      .spec.template.spec.containers[] | select(.name == "node-agent") |
      (.args | index("--release-version=" + $version) != null) and
      (.args | index("--release-manifest-digest=" + $digest) != null)
    ' >/dev/null
  receipt_ready=false
  for _ in $(seq 1 30); do
    if docker exec "$node" cat /var/lib/cni/waycloak/install-receipt.json 2>/dev/null | \
      jq -e --arg version "$expected_version" --arg digest "$expected_digest" '
        .releaseIdentity.version == $version and .releaseIdentity.manifestDigest == $digest
      ' >/dev/null; then
      receipt_ready=true
      break
    fi
    sleep 1
  done
  if [[ "$receipt_ready" != true ]]; then
    printf '%s transition did not publish the exact CNI receipt\n' "$label" >&2
    return 1
  fi
  doctor_deadline="$((SECONDS + 120))"
  until "$work_dir/waycloakctl" doctor --output json >"$work_dir/doctor-${label}.json"; do
    if (( SECONDS >= doctor_deadline )); then
      printf '%s transition did not restore healthy node capability\n' "$label" >&2
      cat "$work_dir/doctor-${label}.json" >&2 || true
      kubectl get nodes -o yaml >&2 || true
      kubectl logs --namespace "$system_namespace" \
        --selector app.kubernetes.io/component=node-agent \
        --all-containers --prefix --tail=200 >&2 || true
      return 1
    fi
    sleep 2
  done
  jq -e '.healthy == true and .nodeCapabilityStates.CNICapable == 1' \
    "$work_dir/doctor-${label}.json" >/dev/null
}

assert_rotation_fail_closed() {
  local label="$1"
  local pod_name="rotation-${label}-probe"
  local doctor_degraded=false
  local startup_denied=false

  for _ in $(seq 1 60); do
    if ! "$work_dir/waycloakctl" doctor --output json \
      >"$work_dir/doctor-rotation-${label}.json" 2>/dev/null && \
      jq -e '.healthy == false and (.nodeCapabilityStates.CNICapable // 0) == 0' \
        "$work_dir/doctor-rotation-${label}.json" >/dev/null; then
      doctor_degraded=true
      break
    fi
    sleep 1
  done
  if [[ "$doctor_degraded" != true ]]; then
    cat "$work_dir/doctor-rotation-${label}.json" >&2 || true
    printf '%s certificate interruption retained stale healthy doctor state\n' "$label" >&2
    return 1
  fi
  test -z "$(kubectl get node -o jsonpath='{.items[0].metadata.labels.networking\.waycloak\.io\.node-restriction\.kubernetes\.io/cni-ready}')"

  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
  namespace: ${smoke_namespace}
  labels:
    networking.waycloak.io/egress-route: transition-guard
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  containers:
    - name: probe
      image: ${probe_ref}
      env:
        - name: PROBE_URL
          value: ${probe_url}
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
EOF
  for _ in $(seq 1 45); do
    kubectl get pod "$pod_name" --namespace "$smoke_namespace" -o json | \
      jq -e '((.status.podIP // "") | length) == 0 and
        ([.status.containerStatuses[]? | select(.state.running or .state.terminated or ((.containerID // "") | length > 0))] | length) == 0' >/dev/null
    if kubectl get events --namespace "$smoke_namespace" \
      --field-selector "involvedObject.kind=Pod,involvedObject.name=${pod_name}" -o json | \
      jq -e '.items[] | select(.reason == "FailedScheduling" or .reason == "FailedCreatePodSandBox")' >/dev/null; then
      startup_denied=true
      break
    fi
    sleep 1
  done
  if [[ "$startup_denied" != true ]]; then
    kubectl describe pod "$pod_name" --namespace "$smoke_namespace" >&2 || true
    printf '%s certificate interruption did not deny enrolled startup\n' "$label" >&2
    return 1
  fi
  kubectl delete pod "$pod_name" --namespace "$smoke_namespace" --wait=true --timeout=2m
}

install_rotation_fault_policy() {
  local mode="$1"
  local expression
  if [[ "$mode" == switch ]]; then
    expression='object.data["tls.crt"] == oldObject.data["tls.crt"]'
  else
    expression='size(object.data["ca.crt"]) >= size(oldObject.data["ca.crt"])'
  fi
  cat <<EOF | kubectl apply -f -
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: waycloak-ci-rotation-fault
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["UPDATE"]
        resources: ["secrets"]
  matchConditions:
    - name: exact-stable-serving-secret
      expression: 'object.metadata.namespace == "${system_namespace}" && object.metadata.name == "${release_name}-observation-tls"'
  validations:
    - expression: '${expression}'
      message: injected exact certificate rotation interruption
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: waycloak-ci-rotation-fault
spec:
  policyName: waycloak-ci-rotation-fault
  validationActions: [Deny]
EOF
  sleep 2
}

remove_rotation_fault_policy() {
  kubectl delete validatingadmissionpolicybinding/waycloak-ci-rotation-fault \
    validatingadmissionpolicy/waycloak-ci-rotation-fault --wait=true --timeout=2m
}

apply_certificate_rotation() {
  local plan_path="$work_dir/certificate-rotation-plan.json"
  local recovered_path="$work_dir/certificate-rotation-recovered.json"
  local carry_plan="$work_dir/install-plan-after-certificate-rotation.json"
  local plan_id before_ca_uid before_tls_uid before_ca before_tls after_ca after_tls ca_count

  before_ca_uid="$(kubectl get secret "${release_name}-observation-ca" --namespace "$system_namespace" -o jsonpath='{.metadata.uid}')"
  before_tls_uid="$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o jsonpath='{.metadata.uid}')"
  before_ca="$(kubectl get secret "${release_name}-observation-ca" --namespace "$system_namespace" -o jsonpath='{.data.ca\.crt}')"
  before_tls="$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o jsonpath='{.data.tls\.crt}')"
  "$work_dir/waycloakctl" certificate rotation plan \
    --namespace "$system_namespace" --release "$release_name" --output json >"$plan_path"
  plan_id="$(jq -r '.planID' "$plan_path")"
  jq -e '
    .kind == "CertificateRotationPlan" and
    .rotationSequence == "ObservationCertificateRotation-v1" and
    .source.state == "Deployed" and
    (.source.observationCAUID | length) > 0 and
    (.source.observationTLSUID | length) > 0 and
    (.planID | test("^sha256:[a-f0-9]{64}$"))
  ' "$plan_path" >/dev/null
  if grep -Eq 'PRIVATE KEY|tls\.key' "$plan_path"; then
    printf 'certificate rotation plan contains private material\n' >&2
    return 1
  fi
  if "$work_dir/waycloakctl" certificate rotation apply --plan "$plan_path" \
    --confirm sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff; then
    printf 'certificate rotation accepted the wrong confirmation\n' >&2
    return 1
  fi
  if kubectl get secret "${release_name}-observation-next" --namespace "$system_namespace" >/dev/null 2>&1; then
    printf 'refused certificate rotation generated staged private material\n' >&2
    return 1
  fi

  install_rotation_fault_policy switch
  if "$work_dir/waycloakctl" certificate rotation apply --plan "$plan_path" --confirm "$plan_id"; then
    printf 'certificate serving-switch fault did not interrupt apply\n' >&2
    return 1
  fi
  kubectl get configmap "${release_name}-certificate-rotation" --namespace "$system_namespace" -o json | \
    jq -e --arg plan "$plan_id" '
      .immutable == true and
      .metadata.annotations["install.waycloak.io/certificate-rotation-plan-id"] == $plan and
      (.data["rotation.json"] | fromjson |
        .plan.planID == $plan and
        (.stagedSecretUID | length) > 0 and
        (.targetCADigest | test("^sha256:[a-f0-9]{64}$")) and
        (.targetServingDigest | test("^sha256:[a-f0-9]{64}$")))
    ' >/dev/null
  if kubectl get configmap "${release_name}-certificate-rotation" --namespace "$system_namespace" \
    -o jsonpath='{.data.rotation\.json}' | grep -Eq 'PRIVATE KEY|tls\.key'; then
    printf 'certificate rotation journal contains private material\n' >&2
    return 1
  fi
  ca_count="$(kubectl get secret "${release_name}-observation-ca" --namespace "$system_namespace" \
    -o jsonpath='{.data.ca\.crt}' | base64 --decode | grep -c 'BEGIN CERTIFICATE')"
  test "$ca_count" = 2
  test "$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o jsonpath='{.data.tls\.crt}')" = "$before_tls"
  kubectl get daemonset/waycloak-node-agent --namespace "$system_namespace" -o json | \
    jq -e --arg plan "${plan_id}-overlap" '
      .spec.template.metadata.annotations["install.waycloak.io/observation-rotation-id"] == $plan and
      any(.spec.template.spec.containers[] | select(.name == "node-agent") | .args[]; . == "--observation-capability-hold=true")
    ' >/dev/null
  assert_rotation_fail_closed overlap
  "$work_dir/waycloakctl" certificate rotation plan \
    --namespace "$system_namespace" --release "$release_name" --output json >"$recovered_path"
  test "$(jq -S . "$plan_path")" = "$(jq -S . "$recovered_path")"
  if "$work_dir/waycloakctl" install plan --release-manifest "$work_dir/baseline-release-manifest.json" \
    --namespace "$system_namespace" --release "$release_name" --output json >/dev/null 2>&1; then
    printf 'install planning crossed an active certificate rotation\n' >&2
    return 1
  fi
  remove_rotation_fault_policy

  install_rotation_fault_policy prune
  if "$work_dir/waycloakctl" certificate rotation apply --plan "$recovered_path" --confirm "$plan_id"; then
    printf 'certificate trust-prune fault did not interrupt apply\n' >&2
    return 1
  fi
  test "$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o jsonpath='{.data.tls\.crt}')" != "$before_tls"
  ca_count="$(kubectl get secret "${release_name}-observation-ca" --namespace "$system_namespace" \
    -o jsonpath='{.data.ca\.crt}' | base64 --decode | grep -c 'BEGIN CERTIFICATE')"
  test "$ca_count" = 2
  assert_rotation_fail_closed serving-switch
  "$work_dir/waycloakctl" certificate rotation plan \
    --namespace "$system_namespace" --release "$release_name" --output json >"$recovered_path"
  test "$(jq -S . "$plan_path")" = "$(jq -S . "$recovered_path")"
  remove_rotation_fault_policy

  "$work_dir/waycloakctl" certificate rotation apply --plan "$recovered_path" --confirm "$plan_id"
  if kubectl get configmap "${release_name}-certificate-rotation" --namespace "$system_namespace" >/dev/null 2>&1 || \
    kubectl get secret "${release_name}-observation-next" --namespace "$system_namespace" >/dev/null 2>&1; then
    printf 'completed certificate rotation retained staged state\n' >&2
    return 1
  fi
  test "$(kubectl get secret "${release_name}-observation-ca" --namespace "$system_namespace" -o jsonpath='{.metadata.uid}')" = "$before_ca_uid"
  test "$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o jsonpath='{.metadata.uid}')" = "$before_tls_uid"
  after_ca="$(kubectl get secret "${release_name}-observation-ca" --namespace "$system_namespace" -o jsonpath='{.data.ca\.crt}')"
  after_tls="$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o jsonpath='{.data.tls\.crt}')"
  test "$after_ca" != "$before_ca"
  test "$after_tls" != "$before_tls"
  test "$after_ca" = "$(kubectl get secret "${release_name}-observation-tls" --namespace "$system_namespace" -o jsonpath='{.data.ca\.crt}')"
  test "$(printf '%s' "$after_ca" | base64 --decode | grep -c 'BEGIN CERTIFICATE')" = 1
  kubectl get daemonset/waycloak-node-agent --namespace "$system_namespace" -o json | \
    jq -e --arg plan "$plan_id" '
      .spec.template.metadata.annotations["install.waycloak.io/observation-rotation-id"] == $plan and
      (any(.spec.template.spec.containers[] | select(.name == "node-agent") | .args[]; startswith("--observation-capability-hold=")) | not)
    ' >/dev/null
  "$work_dir/waycloakctl" doctor --output json >"$work_dir/doctor-rotation-complete.json"
  jq -e '.healthy == true and .nodeCapabilityStates.CNICapable == 1' "$work_dir/doctor-rotation-complete.json" >/dev/null
  "$work_dir/waycloakctl" install plan --release-manifest "$work_dir/baseline-release-manifest.json" \
    --namespace "$system_namespace" --release "$release_name" --output json >"$carry_plan"
  jq -e --arg plan "$plan_id" '
    .source.observationRotationID == $plan and
    (.valuesYAML | contains("observationRotationID: \"" + $plan + "\""))
  ' "$carry_plan" >/dev/null
}

create_port_forward_controller_secret() {
  local generation="$1"
  local identity_dir="$work_dir/port-forward-controller-${generation}"
  mkdir -p "$identity_dir"
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -keyout "$identity_dir/ca.key" \
    -out "$identity_dir/ca.crt" \
    -subj "/CN=Waycloak port-forward ${generation} CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null
  cat >"$identity_dir/client.conf" <<EOF
[req]
distinguished_name = subject
req_extensions = extensions
prompt = no
[subject]
CN = Waycloak replacement controller
[extensions]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = clientAuth
subjectAltName = URI:spiffe://waycloak.io/replacement-controller
EOF
  openssl req -newkey rsa:2048 -nodes \
    -keyout "$identity_dir/tls.key" \
    -out "$identity_dir/tls.csr" \
    -config "$identity_dir/client.conf" >/dev/null
  openssl x509 -req -days 1 -sha256 \
    -in "$identity_dir/tls.csr" \
    -CA "$identity_dir/ca.crt" \
    -CAkey "$identity_dir/ca.key" \
    -CAcreateserial \
    -out "$identity_dir/tls.crt" \
    -extfile "$identity_dir/client.conf" \
    -extensions extensions >/dev/null
  openssl verify -purpose sslclient -CAfile "$identity_dir/ca.crt" \
    "$identity_dir/tls.crt" >/dev/null
  kubectl create secret generic waycloak-port-forward-controller-tls \
    --namespace "$system_namespace" \
    --type kubernetes.io/tls \
    --from-file=ca.crt="$identity_dir/ca.crt" \
    --from-file=tls.crt="$identity_dir/tls.crt" \
    --from-file=tls.key="$identity_dir/tls.key" \
    --dry-run=client -o json | jq '.immutable = true' | kubectl create -f - >/dev/null
}

apply_port_forward_capability() {
  local plan_path="$work_dir/port-forward-install-plan.json"
  local rebound_plan_path="$work_dir/port-forward-install-plan-rebound.json"
  local class_uid secret_uid plan_id rebound_plan_id

  create_port_forward_controller_secret first
  class_uid="$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.metadata.uid}')"
  "$work_dir/waycloakctl" install plan \
    --release-manifest "$work_dir/port-forward-release-manifest.json" \
    --namespace "$system_namespace" \
    --release "$release_name" \
    --enable-port-forwarding \
    --port-forward-controller-tls-secret waycloak-port-forward-controller-tls \
    --enable-adapter-protocol \
    --output json >"$plan_path"
  plan_id="$(jq -r '.planID' "$plan_path")"
  secret_uid="$(kubectl get secret waycloak-port-forward-controller-tls --namespace "$system_namespace" -o jsonpath='{.metadata.uid}')"
  jq -e --arg uid "$secret_uid" '
    .operation == "ExactReleaseTransition" and
    .portForwarding.secretUID == $uid and
    .portForwarding.adapterProtocolEnabled == true and
    .metadata.optionalCapability == "networking.waycloak.io/PortForwardServiceSingleActive" and
    .targetRelease.profiles == ["networking.waycloak.io/Core-v1"] and
    (.valuesYAML | contains("conformanceProfile: \"networking.waycloak.io/Core-v1\"")) and
    (.valuesYAML | contains("portForwarding:\n  enabled: true"))
  ' "$plan_path" >/dev/null
  if grep -Eq 'PRIVATE KEY|tls\.key' "$plan_path"; then
    printf 'Port-forward install plan exposed private key material\n' >&2
    return 1
  fi
  if "$work_dir/waycloakctl" install apply --plan "$plan_path" \
    --confirm sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff; then
    printf 'Port-forward install accepted the wrong confirmation\n' >&2
    return 1
  fi
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.metadata.uid}')" = "$class_uid"

  kubectl delete secret waycloak-port-forward-controller-tls --namespace "$system_namespace" --wait=true
  create_port_forward_controller_secret second
  if "$work_dir/waycloakctl" install apply --plan "$plan_path" --confirm "$plan_id"; then
    printf 'Port-forward install accepted a replaced TLS Secret identity\n' >&2
    return 1
  fi
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.metadata.uid}')" = "$class_uid"

  "$work_dir/waycloakctl" install plan \
    --release-manifest "$work_dir/port-forward-release-manifest.json" \
    --namespace "$system_namespace" \
    --release "$release_name" \
    --enable-port-forwarding \
    --port-forward-controller-tls-secret waycloak-port-forward-controller-tls \
    --enable-adapter-protocol \
    --output json >"$rebound_plan_path"
  rebound_plan_id="$(jq -r '.planID' "$rebound_plan_path")"
  test "$rebound_plan_id" != "$plan_id"
  "$work_dir/waycloakctl" install apply --plan "$rebound_plan_path" --confirm "$rebound_plan_id"

  kubectl rollout status deployment/waycloak-controller --namespace "$system_namespace" --timeout=2m
  kubectl rollout status daemonset/waycloak-cni-installer --namespace "$system_namespace" --timeout=2m
  kubectl rollout status daemonset/waycloak-node-agent --namespace "$system_namespace" --timeout=2m
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.spec.conformanceProfile}')" = \
    networking.waycloak.io/Core-v1
  kubectl get vpngatewayclass gluetun.waycloak.io -o json | jq -e '
    (.spec.supportedFeatures | index("networking.waycloak.io/PortForwardServiceSingleActive")) != null and
    (.spec.supportedFeatures | index("networking.waycloak.io/WorkloadAdapter")) != null
  ' >/dev/null
  kubectl get deployment/waycloak-controller --namespace "$system_namespace" -o json | jq -e --arg runtime "$gateway_runtime_ref" '
    any(.spec.template.spec.containers[] | select(.name == "controller") | .args[]; . == "--gateway-port-forward-runtime-image=" + $runtime) and
    any(.spec.template.spec.containers[] | select(.name == "controller") | .args[]; . == "--conformance-profile=networking.waycloak.io/Core-v1") and
    any(.spec.template.spec.volumes[]; .name == "port-forward-tls" and .secret.secretName == "waycloak-port-forward-controller-tls")
  ' >/dev/null
  test "$(kubectl get statefulset --namespace "$smoke_namespace" -l app.kubernetes.io/component=gateway -o json | jq '[.items[].spec.template.spec.containers[]] | length')" = 2
}

run_disruptive_verify baseline
cat <<EOF | kubectl apply -f -
apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: transition-guard
  namespace: ${smoke_namespace}
spec:
  parentRefs:
    - group: networking.waycloak.io
      kind: VPNGateway
      namespace: ${smoke_namespace}
      name: disposable
  requiredFeatures:
    - networking.waycloak.io/TCP
EOF
kubectl wait vpnegressroute/transition-guard --namespace "$smoke_namespace" \
  --for=condition=Ready --timeout=2m
apply_exact_transition forward "$work_dir/release-manifest.json" "$release_version"
run_disruptive_verify forward
apply_exact_transition rollback "$work_dir/baseline-release-manifest.json" "$baseline_release_version"
run_disruptive_verify rollback
apply_certificate_rotation
run_disruptive_verify certificate-rotation
kubectl delete vpnegressroute/transition-guard --namespace "$smoke_namespace" \
  --wait=true --timeout=2m

cat <<EOF | kubectl apply -f -
apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: recovery
  namespace: ${smoke_namespace}
spec:
  parentRefs:
    - group: networking.waycloak.io
      kind: VPNGateway
      namespace: ${smoke_namespace}
      name: disposable
  requiredFeatures:
    - networking.waycloak.io/TCP
EOF
kubectl wait vpnegressroute/recovery --namespace "$smoke_namespace" \
  --for=condition=Ready --timeout=2m

old_gateway_uid="$(kubectl get vpngateway/disposable --namespace "$smoke_namespace" -o jsonpath='{.metadata.uid}')"
"$work_dir/waycloakctl" state backup --output json >"$work_dir/state-backup.json"
jq -e '
  .kind == "StateBackup" and
  (.backupID | test("^sha256:[a-f0-9]{64}$")) and
  [.resources[].kind] == ["VPNGateway", "VPNEgressRoute"] and
  ([.resources[] | has("status")] | all(. == false)) and
  ([.resources[] | .kind == "VPNWorkloadBinding"] | any) == false
' "$work_dir/state-backup.json" >/dev/null

kubectl delete vpnegressroute/recovery vpngateway/disposable \
  --namespace "$smoke_namespace" --wait=true --timeout=2m
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: recovery-probe
  namespace: ${smoke_namespace}
  labels:
    networking.waycloak.io/egress-route: recovery
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  containers:
    - name: probe
      image: ${probe_ref}
      env:
        - name: PROBE_URL
          value: ${probe_url}
        - name: PROBE_CA_FILE
          value: /observer-ca/ca.crt
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      volumeMounts:
        - name: observer-ca
          mountPath: /observer-ca
          readOnly: true
  volumes:
    - name: observer-ca
      configMap:
        name: observer-ca
EOF

failed_sandbox_observed=false
for _ in $(seq 1 30); do
  if [[ "$(kubectl get pod/recovery-probe --namespace "$smoke_namespace" -o json | \
    jq '[.status.containerStatuses[]? | select(.state.running or .state.terminated or ((.containerID // "") | length > 0))] | length')" != "0" ]]; then
    printf 'recovery probe application container started before portable intent restore\n' >&2
    exit 1
  fi
  if kubectl get events --namespace "$smoke_namespace" \
    --field-selector involvedObject.kind=Pod,involvedObject.name=recovery-probe -o json | \
    jq -e '.items[] | select(.reason == "FailedCreatePodSandBox") | .message | test("waycloak|egress route|binding"; "i")' >/dev/null; then
    failed_sandbox_observed=true
    break
  fi
  sleep 1
done
if [[ "$failed_sandbox_observed" != true ]]; then
  kubectl describe pod/recovery-probe --namespace "$smoke_namespace" >&2
  printf 'missing-route recovery window did not produce a Waycloak sandbox failure\n' >&2
  exit 1
fi

"$work_dir/waycloakctl" state restore plan \
  --backup "$work_dir/state-backup.json" \
  --overlay-cidr 100.96.0.0/16 \
  --output json >"$work_dir/state-restore-plan.json"
restore_plan_id="$(jq -r '.planID' "$work_dir/state-restore-plan.json")"
if "$work_dir/waycloakctl" state restore apply \
  --plan "$work_dir/state-restore-plan.json" \
  --confirm sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff; then
  printf 'wrong state-restore confirmation unexpectedly mutated the cluster\n' >&2
  exit 1
fi
test "$(kubectl get vpngateway/disposable --namespace "$smoke_namespace" --ignore-not-found -o name)" = ""
"$work_dir/waycloakctl" state restore apply \
  --plan "$work_dir/state-restore-plan.json" \
  --confirm "$restore_plan_id"
"$work_dir/waycloakctl" state restore apply \
  --plan "$work_dir/state-restore-plan.json" \
  --confirm "$restore_plan_id"

kubectl wait vpngateway/disposable --namespace "$smoke_namespace" \
  --for=condition=Ready --timeout=3m
kubectl wait vpnegressroute/recovery --namespace "$smoke_namespace" \
  --for=condition=Ready --timeout=2m
restored_gateway_json="$(kubectl get vpngateway/disposable --namespace "$smoke_namespace" \
  --show-managed-fields -o json)"
new_gateway_uid="$(jq -r '.metadata.uid' <<<"$restored_gateway_json")"
if [[ -z "$old_gateway_uid" || -z "$new_gateway_uid" || "$old_gateway_uid" == "$new_gateway_uid" ]]; then
  printf 'portable restore did not reacquire a fresh gateway UID\n' >&2
  exit 1
fi
actual_restore_plan_id="$(jq -r '.metadata.annotations["state.waycloak.io/restore-plan-id"] // ""' <<<"$restored_gateway_json")"
if [[ "$actual_restore_plan_id" != "$restore_plan_id" ]]; then
  printf 'restored gateway is not bound to the exact reviewed plan: expected=%s actual=%s\n' \
    "$restore_plan_id" "$actual_restore_plan_id" >&2
  exit 1
fi
if ! jq -e 'any(.metadata.managedFields[]?; .manager == "waycloakctl-state-restore" and .operation == "Apply")' \
  <<<"$restored_gateway_json" >/dev/null; then
  printf 'restored gateway lacks server-side apply ownership by waycloakctl-state-restore\n' >&2
  jq '{annotations: .metadata.annotations, managedFields: .metadata.managedFields}' \
    <<<"$restored_gateway_json" >&2
  exit 1
fi

kubectl wait pod/recovery-probe --namespace "$smoke_namespace" \
  --for=jsonpath='{.status.phase}'=Succeeded --timeout=3m
recovery_pod_uid="$(kubectl get pod/recovery-probe --namespace "$smoke_namespace" -o jsonpath='{.metadata.uid}')"
binding_uid=""
for _ in $(seq 1 60); do
  binding_uid="$(kubectl get vpnworkloadbindings --namespace "$smoke_namespace" -o json | \
    jq -r --arg uid "$recovery_pod_uid" '.items[] | select(.spec.podRef.uid == $uid) | .metadata.uid' | head -n1)"
  [[ -n "$binding_uid" ]] && break
  sleep 1
done
if [[ -z "$binding_uid" ]]; then
  printf 'portable restore did not reacquire a fresh UID-bound allocation\n' >&2
  exit 1
fi

kubectl delete pod/recovery-probe vpnegressroute/recovery --namespace "$smoke_namespace" \
  --wait=true --timeout=2m

apply_port_forward_capability

printf 'exact-artifact Kind install apply completed in %ss\n' "$apply_elapsed"
