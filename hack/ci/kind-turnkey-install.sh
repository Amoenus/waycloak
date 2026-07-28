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
readonly system_namespace="waycloak-system"
readonly release_name="waycloak"

work_dir="$(mktemp -d)"

cleanup() {
  status="$?"
  if (( status != 0 )) && kind get clusters 2>/dev/null | grep -qx "$cluster_name"; then
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
docker exec "$node" grep -q '"agentSocket": "/run/waycloak/cni-agent.sock"' /etc/cni/net.d/10-kindnet.conflist
docker exec "$node" grep -q '"agentKeyFile": "/run/waycloak/cni-auth.key"' /etc/cni/net.d/10-kindnet.conflist
docker exec "$node" test -S /run/waycloak/cni-agent.sock
docker exec "$node" test -f /run/waycloak/cni-auth.key
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
before_owned_pods="$(kubectl get pods --namespace "$smoke_namespace" \
  --selector app.kubernetes.io/managed-by=waycloakctl --no-headers 2>/dev/null | wc -l)"
if "$work_dir/waycloakctl" verify \
  --namespace "$smoke_namespace" \
  --gateway disposable \
  --probe-image "$probe_ref" \
  --probe-url "$probe_url" \
  --probe-ca-configmap observer-ca \
  --confirm sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff \
  --output json >"$work_dir/refused-verify.json" 2>"$work_dir/refused-verify-error.txt"; then
  printf 'wrong disruptive verification confirmation unexpectedly succeeded\n' >&2
  exit 1
fi
required_confirmation="$(grep -Eo 'sha256:[a-f0-9]{64}' "$work_dir/refused-verify-error.txt" | tail -n1)"
if [[ ! "$required_confirmation" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  cat "$work_dir/refused-verify-error.txt" >&2
  printf 'refused verification did not report its exact identity\n' >&2
  exit 1
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
  --output json >"$work_dir/verify.json"
jq -e '.verified == true and .distinctEgress == true and
  .protectedSucceeded == true and .ordinarySucceeded == true and
  .tunnelLossVerified == true and .cleanupComplete == true' \
  "$work_dir/verify.json" >/dev/null
test "$(kubectl get vpnegressroutes --namespace "$smoke_namespace" \
  --selector app.kubernetes.io/managed-by=waycloakctl --no-headers 2>/dev/null | wc -l)" = "0"

printf 'exact-artifact Kind install apply completed in %ss\n' "$apply_elapsed"
