#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

if [[ "$#" -ne 4 ]]; then
  echo "usage: verify-release.sh RELEASE_TAG SOURCE_SHA REPOSITORY ASSET_DIR" >&2
  exit 2
fi

readonly release_tag="$1"
readonly source_sha="$2"
readonly repository="$3"
readonly asset_dir="$4"
readonly issuer="https://token.actions.githubusercontent.com"
readonly identity="https://github.com/${repository}/.github/workflows/waycloak-release.yaml@refs/tags/${release_tag}"
readonly signer_workflow="${repository}/.github/workflows/waycloak-release.yaml"
readonly chart_archive="waycloak-${release_tag#v}.tgz"
readonly kcl_archive="waycloak-kcl-${release_tag}.tar"
readonly gluetun_upstream_commit="7eed6eaf160440724a93ca66f66055068cebe4ac"
readonly gluetun_upstream_image="docker.io/qmcgaw/gluetun@sha256:e3272b29a4bc177b389fbdcb54cf9716ccbfc30f04d8b7a35b0a5be9cdb58461"

bash "$(dirname -- "${BASH_SOURCE[0]}")/validate-release-tag.sh" "$release_tag"
if [[ ! "$source_sha" =~ ^[a-f0-9]{40}$ ]]; then
  echo "source SHA must be one exact lowercase Git commit" >&2
  exit 1
fi
if [[ ! "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "repository must be OWNER/NAME" >&2
  exit 1
fi
if [[ ! -d "$asset_dir" ]]; then
  echo "asset directory does not exist" >&2
  exit 1
fi

for command_name in cmp cosign crane gh go jq sha256sum tail tar timeout; do
  if ! command -v "$command_name" >/dev/null; then
    echo "required command is unavailable: ${command_name}" >&2
    exit 1
  fi
done

work_dir="$(mktemp -d)"
cleanup() {
  rm -rf -- "$work_dir"
}
trap cleanup EXIT

retry_bounded_quiet() {
  local description="$1"
  shift
  local attempt
  for attempt in 1 2; do
    echo "${description}: attempt ${attempt}/2" >&2
    if timeout --kill-after=15s 2m "$@" \
      >"$work_dir/command.stdout" 2>"$work_dir/command.stderr"; then
      echo "${description}: verified" >&2
      return 0
    fi
    tail -c 16384 "$work_dir/command.stdout" >&2 || true
    tail -c 16384 "$work_dir/command.stderr" >&2 || true
  done
  echo "${description}: failed after two bounded attempts" >&2
  return 1
}

retry_bounded_to_file() {
  local description="$1"
  local output_path="$2"
  shift 2
  local attempt
  for attempt in 1 2; do
    echo "${description}: attempt ${attempt}/2" >&2
    if timeout --kill-after=15s 2m "$@" >"${output_path}.partial"; then
      mv -- "${output_path}.partial" "$output_path"
      return 0
    fi
    rm -f -- "${output_path}.partial"
  done
  echo "${description}: failed after two bounded attempts" >&2
  return 1
}

expected_assets=(
  SHA256SUMS
  SHA256SUMS.sigstore.json
  release-manifest.json
  release-manifest.sigstore.json
  gluetun-binaries.SHA256SUMS
  gluetun-control-auth.toml
  gluetun-dependency.patch
  gluetun.LICENSE
  gluetun.ref
  gluetun.spdx.json
  replacement-controller.ref
  replacement-controller.spdx.json
  waycloak-cni.ref
  waycloak-cni.spdx.json
  waycloak-node-agent.ref
  waycloak-node-agent.spdx.json
  waycloak-gateway-agent.ref
  waycloak-gateway-agent.spdx.json
  waycloak-gateway-runtime.ref
  waycloak-gateway-runtime.spdx.json
  waycloak-qbittorrent-adapter.ref
  waycloak-qbittorrent-adapter.spdx.json
  waycloak-chart.ref
  waycloak-chart.spdx.json
  waycloak-kcl.ref
  waycloak-kcl.spdx.json
  "$chart_archive"
  "$kcl_archive"
)
for artifact in "${expected_assets[@]}"; do
  test -f "$asset_dir/$artifact"
done

(
  cd "$asset_dir"
  sha256sum --check SHA256SUMS
)
retry_bounded_quiet "release manifest blob signature" cosign verify-blob \
  --bundle "$asset_dir/release-manifest.sigstore.json" \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$asset_dir/release-manifest.json"
retry_bounded_quiet "checksum inventory blob signature" cosign verify-blob \
  --bundle "$asset_dir/SHA256SUMS.sigstore.json" \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$asset_dir/SHA256SUMS"

jq -e --arg version "$release_tag" \
  '.version == $version and .apiVersion == "release.waycloak.io/v1"' \
  "$asset_dir/release-manifest.json" >/dev/null
bash "$(dirname -- "${BASH_SOURCE[0]}")/validate-release-inventory.sh" \
  "$asset_dir/release-manifest.json"
test "$(jq -r '.chart.repository + "@" + .chart.digest' "$asset_dir/release-manifest.json")" = \
  "$(cat "$asset_dir/waycloak-chart.ref")"
test "$(jq -r '.kcl.repository + "@" + .kcl.digest' "$asset_dir/release-manifest.json")" = \
  "$(cat "$asset_dir/waycloak-kcl.ref")"

image_ref_files=(
  gluetun.ref
  replacement-controller.ref
  waycloak-cni.ref
  waycloak-node-agent.ref
  waycloak-gateway-agent.ref
  waycloak-gateway-runtime.ref
  waycloak-qbittorrent-adapter.ref
)
for ref_file in "${image_ref_files[@]}"; do
  image_name="${ref_file%.ref}"
  reference="$(cat "$asset_dir/$ref_file")"
  test "$(jq -r --arg name "$image_name" \
    '.images[$name].repository + "@" + .images[$name].digest' \
    "$asset_dir/release-manifest.json")" = "$reference"
  echo "verifying ${ref_file}: ${reference}" >&2
  retry_bounded_quiet "${ref_file} signature" cosign verify \
    --certificate-identity "$identity" \
    --certificate-oidc-issuer "$issuer" \
    "$reference"
  retry_bounded_quiet "${ref_file} SPDX attestation" cosign verify-attestation \
    --type spdxjson \
    --certificate-identity "$identity" \
    --certificate-oidc-issuer "$issuer" \
    "$reference"
  retry_bounded_to_file "${ref_file} platform index" "$work_dir/${image_name}.manifest.json" \
    crane manifest "$reference"
  jq -e \
    '[.manifests[].platform | .os + "/" + .architecture] | sort == ["linux/amd64", "linux/arm64"]' \
    "$work_dir/${image_name}.manifest.json" >/dev/null
  retry_bounded_quiet "${ref_file} GitHub provenance" gh attestation verify "oci://$reference" \
    --repo "$repository" \
    --signer-workflow "$signer_workflow" \
    --source-ref "refs/tags/${release_tag}" \
    --source-digest "$source_sha" \
    --deny-self-hosted-runners
done

gluetun_reference="$(cat "$asset_dir/gluetun.ref")"
for architecture in amd64 arm64; do
  expected_checksum="$(awk -v architecture="$architecture" \
    '$2 == "bin/gluetun-entrypoint-linux-" architecture {print $1}' \
    "$asset_dir/gluetun-binaries.SHA256SUMS")"
  [[ "$expected_checksum" =~ ^[a-f0-9]{64}$ ]]
  retry_bounded_to_file "Gluetun linux/${architecture} config" "$work_dir/gluetun-${architecture}.config.json" \
    crane config --platform "linux/$architecture" "$gluetun_reference"
  jq -e \
    --arg commit "$gluetun_upstream_commit" \
    --arg image "$gluetun_upstream_image" \
    '.config.Labels["io.waycloak.gluetun.upstream-commit"] == $commit and
     .config.Labels["io.waycloak.gluetun.upstream-image"] == $image' \
    "$work_dir/gluetun-${architecture}.config.json" >/dev/null
  retry_bounded_to_file "Gluetun linux/${architecture} filesystem" "$work_dir/gluetun-${architecture}.tar" \
    crane export --platform "linux/$architecture" "$gluetun_reference" -
  tar -xOf "$work_dir/gluetun-${architecture}.tar" gluetun-entrypoint \
    >"$work_dir/gluetun-entrypoint-linux-${architecture}"
  printf '%s  %s\n' "$expected_checksum" "$work_dir/gluetun-entrypoint-linux-${architecture}" | \
    sha256sum --check
  tar -xOf "$work_dir/gluetun-${architecture}.tar" etc/waycloak/gluetun-control-auth.toml \
    >"$work_dir/gluetun-control-auth-${architecture}.toml"
  cmp "$asset_dir/gluetun-control-auth.toml" "$work_dir/gluetun-control-auth-${architecture}.toml"
done

chart_reference="$(sed 's|^oci://||' "$asset_dir/waycloak-chart.ref")"
retry_bounded_quiet "chart signature" cosign verify \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$chart_reference"
retry_bounded_quiet "chart SPDX attestation" cosign verify-attestation \
  --type spdxjson \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$chart_reference"
retry_bounded_quiet "chart GitHub provenance" gh attestation verify "oci://$chart_reference" \
  --repo "$repository" \
  --signer-workflow "$signer_workflow" \
  --source-ref "refs/tags/${release_tag}" \
  --source-digest "$source_sha" \
  --deny-self-hosted-runners
retry_bounded_to_file "chart manifest" "$work_dir/chart.manifest.json" \
  crane manifest "$chart_reference"
jq -e \
  '.layers | length == 1 and .[0].mediaType == "application/vnd.cncf.helm.chart.content.v1.tar+gzip"' \
  "$work_dir/chart.manifest.json" >/dev/null
chart_layer_digest="$(jq -r '.layers[0].digest' "$work_dir/chart.manifest.json")"
[[ "$chart_layer_digest" =~ ^sha256:[a-f0-9]{64}$ ]]
chart_repository="${chart_reference%@sha256:*}"
retry_bounded_to_file "exact chart layer" "$work_dir/$chart_archive" \
  crane blob "${chart_repository}@${chart_layer_digest}"
cmp "$asset_dir/$chart_archive" "$work_dir/$chart_archive"
test "$(tar -tzf "$asset_dir/$chart_archive" | grep -Ec \
  '^waycloak/crds/networking\.waycloak\.io_(portforwardleases|vpnegressroutes|vpngatewayclasses|vpngateways|vpnworkloadbindings|workloadadapters)\.yaml$')" -eq 6

kcl_reference="$(sed 's|^oci://||' "$asset_dir/waycloak-kcl.ref")"
retry_bounded_quiet "KCL module signature" cosign verify \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$kcl_reference"
retry_bounded_quiet "KCL module SPDX attestation" cosign verify-attestation \
  --type spdxjson \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$kcl_reference"
retry_bounded_quiet "KCL module GitHub provenance" gh attestation verify "oci://$kcl_reference" \
  --repo "$repository" \
  --signer-workflow "$signer_workflow" \
  --source-ref "refs/tags/${release_tag}" \
  --source-digest "$source_sha" \
  --deny-self-hosted-runners
retry_bounded_to_file "KCL module manifest" "$work_dir/kcl.manifest.json" \
  crane manifest "$kcl_reference"
jq -e --arg version "${release_tag#v}" '
  .schemaVersion == 2 and
  .mediaType == "application/vnd.oci.image.manifest.v1+json" and
  .artifactType == "application/vnd.oci.image.layer.v1.tar" and
  .annotations["org.kcllang.package.name"] == "waycloak" and
  .annotations["org.kcllang.package.version"] == $version
' "$work_dir/kcl.manifest.json" >/dev/null
test "$(crane digest "${kcl_reference%@sha256:*}:${release_tag#v}")" = "${kcl_reference##*@}"
kcl_consumer="$work_dir/kcl-consumer"
mkdir -p "$kcl_consumer"
(cd "$kcl_consumer" && kcl mod init consumer >/dev/null)
(cd "$kcl_consumer/consumer" && kcl mod add \
  "oci://${kcl_reference%@sha256:*}" --tag "${release_tag#v}" >/dev/null)
cat >"$kcl_consumer/consumer/main.k" <<'EOF'
import waycloak.v1beta1 as networking

route = networking.VPNEgressRoute {
    metadata = {
        name = "private"
        namespace = "media"
    }
    spec.parentRefs = [{
        group = "networking.waycloak.io"
        kind = "VPNGateway"
        name = "private"
        namespace = "media"
    }]
}
EOF
retry_bounded_to_file "KCL module consumer render" "$work_dir/kcl-consumer.yaml" \
  kcl run "$kcl_consumer/consumer"
grep -q '^  apiVersion: networking.waycloak.io/v1beta1$' "$work_dir/kcl-consumer.yaml"
grep -q '^  kind: VPNEgressRoute$' "$work_dir/kcl-consumer.yaml"

while read -r artifact; do
  retry_bounded_quiet "${artifact} GitHub provenance" gh attestation verify "$asset_dir/$artifact" \
    --repo "$repository" \
    --signer-workflow "$signer_workflow" \
    --source-ref "refs/tags/${release_tag}" \
    --source-digest "$source_sha" \
    --deny-self-hosted-runners
done < <(awk '{print $2}' "$asset_dir/SHA256SUMS")
retry_bounded_quiet "SHA256SUMS GitHub provenance" gh attestation verify "$asset_dir/SHA256SUMS" \
  --repo "$repository" \
  --signer-workflow "$signer_workflow" \
  --source-ref "refs/tags/${release_tag}" \
  --source-digest "$source_sha" \
  --deny-self-hosted-runners

go test ./hack/release ./internal/waycloakctl
echo "exact Waycloak release ${release_tag} verified at ${source_sha}"
