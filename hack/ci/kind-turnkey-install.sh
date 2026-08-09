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
readonly pause_ref="registry.k8s.io/pause@sha256:278fb9dbcca9518083ad1e11276933a2e96f23de604a3a08cc3c80002767d24c"
readonly release_version="v0.0.0-turnkey-ci"
readonly baseline_release_version="v0.0.0-turnkey-ci-baseline"
readonly system_namespace="waycloak-system"
readonly release_name="waycloak"

work_dir="$(mktemp -d)"

cleanup() {
  status="$?"
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
gluetun_ref="$(publish_image fake-gluetun ./test/fixtures/fake-gluetun)"
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

go run ./hack/corerelease \
  --version "$baseline_release_version" \
  --chart "$baseline_chart_ref" \
  --image "replacement-controller=$controller_ref" \
  --image "waycloak-cni=$cni_ref" \
  --image "waycloak-node-agent=$node_agent_ref" \
  --image "waycloak-gateway-agent=$gateway_agent_ref" \
  --image "gluetun=$gluetun_ref" \
  --image "pause=$pause_ref" \
  >"$work_dir/baseline-release-manifest.json"

CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X main.version=${release_version}" \
  -o "$work_dir/waycloakctl" ./cmd/waycloakctl

"$work_dir/waycloakctl" preflight --output json >"$work_dir/preflight.json"
jq -e '.compatible == true and .profile == "networking.waycloak.io/Core-v1"' \
  "$work_dir/preflight.json" >/dev/null

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
  (.valuesYAML | contains("kubernetes.io/arch:"))
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
  --for=jsonpath='{.metadata.labels.networking\.waycloak\.io\.node-restriction\.kubernetes\.io/core-ready}'=true \
  --timeout=2m

manifest_digest="$(jq -r '.manifestDigest' "$work_dir/baseline-release-manifest.json")"
test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.spec.releaseIdentity.version}')" = "$baseline_release_version"
test "$(kubectl get vpngatewayclass gluetun.waycloak.io -o jsonpath='{.spec.releaseIdentity.manifestDigest}')" = "$manifest_digest"
test "$(kubectl get deployment waycloak-controller -n "$system_namespace" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$controller_ref"
test "$(kubectl get daemonset waycloak-cni-installer -n "$system_namespace" -o jsonpath='{.spec.template.spec.initContainers[0].image}')" = "$cni_ref"
test "$(kubectl get daemonset waycloak-node-agent -n "$system_namespace" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$node_agent_ref"
test "$(kubectl get daemonset waycloak-cni-installer -n "$system_namespace" -o jsonpath='{.spec.template.spec.nodeSelector.kubernetes\.io/arch}')" = amd64
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

readonly smoke_namespace="waycloak-smoke"
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

probe_url="https://${observer_ip}:${observer_port}/ip"

run_disruptive_verify() {
  local label="$1"
  local before_owned_pods after_owned_pods required_confirmation
  before_owned_pods="$(kubectl get pods --namespace "$smoke_namespace" \
    --selector app.kubernetes.io/managed-by=waycloakctl --no-headers 2>/dev/null | wc -l)"
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

  "$work_dir/waycloakctl" verify \
    --namespace "$smoke_namespace" \
    --gateway disposable \
    --probe-image "$probe_ref" \
    --probe-url "$probe_url" \
    --probe-ca-configmap observer-ca \
    --confirm "$required_confirmation" \
    --output json >"$work_dir/verify-${label}.json"
  cp "$work_dir/verify-${label}.json" "$work_dir/verify.json"
  jq -e '.verified == true and .distinctEgress == true and
    .protectedSucceeded == true and .ordinarySucceeded == true and
    .tunnelLossVerified == true and .cleanupComplete == true' \
    "$work_dir/verify-${label}.json" >/dev/null
  test "$(kubectl get vpnegressroutes --namespace "$smoke_namespace" \
    --selector app.kubernetes.io/managed-by=waycloakctl --no-headers 2>/dev/null | wc -l)" = "0"
}

apply_exact_transition() {
  local label="$1"
  local manifest_path="$2"
  local expected_version="$3"
  local plan_path="$work_dir/install-plan-${label}.json"
  local expected_digest current_version before_revision after_revision plan_id
  local before_ca before_tls after_ca after_tls receipt_ready

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

  "$work_dir/waycloakctl" install apply --plan "$plan_path" --confirm "$plan_id"
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
  test "$after_ca" = "$before_ca"
  test "$after_tls" = "$before_tls"
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io \
    -o jsonpath='{.spec.releaseIdentity.version}')" = "$expected_version"
  test "$(kubectl get vpngatewayclass gluetun.waycloak.io \
    -o jsonpath='{.spec.releaseIdentity.manifestDigest}')" = "$expected_digest"
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
      return 1
    fi
    sleep 2
  done
  jq -e '.healthy == true and .nodeCapabilityStates.CNICapable == 1' \
    "$work_dir/doctor-${label}.json" >/dev/null
}

run_disruptive_verify baseline
apply_exact_transition forward "$work_dir/release-manifest.json" "$release_version"
run_disruptive_verify forward
apply_exact_transition rollback "$work_dir/baseline-release-manifest.json" "$baseline_release_version"
run_disruptive_verify rollback

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

printf 'exact-artifact Kind install apply completed in %ss\n' "$apply_elapsed"
