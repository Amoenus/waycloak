#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

if [[ "$#" -ne 6 ]]; then
  echo "usage: build-gluetun-candidate.sh SOURCE_DIR SERVERS_SOURCE_DIR OUTPUT_DIR UPSTREAM_COMMIT SERVERS_COMMIT RELEASE_VERSION" >&2
  exit 2
fi

source_dir="$1"
servers_source_dir="$2"
output_dir="$3"
upstream_commit="$4"
servers_commit="$5"
release_version="$6"
go_bin="${GO:-go}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! "$upstream_commit" =~ ^[a-f0-9]{40}$ ]]; then
  echo "upstream commit must be one exact lowercase Git commit" >&2
  exit 1
fi
if [[ ! "$servers_commit" =~ ^[a-f0-9]{40}$ ]]; then
  echo "servers commit must be one exact lowercase Git commit" >&2
  exit 1
fi
bash "$script_dir/validate-release-tag.sh" "$release_version"
if [[ "$(git -C "$source_dir" rev-parse HEAD)" != "$upstream_commit" ]] || \
  [[ -n "$(git -C "$source_dir" status --porcelain)" ]]; then
  echo "Gluetun source must be the clean exact upstream commit" >&2
  exit 1
fi
if [[ "$(git -C "$servers_source_dir" rev-parse HEAD)" != "$servers_commit" ]] || \
  [[ -n "$(git -C "$servers_source_dir" status --porcelain)" ]]; then
  echo "gluetun-servers source must be the clean exact upstream commit" >&2
  exit 1
fi

mkdir -p "$output_dir"
GOWORK=off "$go_bin" run "$script_dir/tools/merge-gluetun-servers/main.go" \
  --base "$source_dir/internal/storage/servers.json" \
  --manifest "$servers_source_dir/pkg/servers/manifest.json" \
  --servers-dir "$servers_source_dir/pkg/servers" \
  --provider protonvpn \
  --output "$source_dir/internal/storage/servers.json"

(
  cd "$source_dir"
  GOWORK=off "$go_bin" get \
    github.com/cloudflare/circl@v1.6.5 \
    golang.org/x/net@v0.58.0 \
    golang.org/x/text@v0.41.0
  GOWORK=off "$go_bin" mod tidy

  test "$(GOWORK=off "$go_bin" list -m -f '{{.Version}}' github.com/cloudflare/circl)" = v1.6.5
  test "$(GOWORK=off "$go_bin" list -m -f '{{.Version}}' golang.org/x/net)" = v0.58.0
  test "$(GOWORK=off "$go_bin" list -m -f '{{.Version}}' golang.org/x/text)" = v0.41.0
  GOWORK=off "$go_bin" run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./cmd/gluetun

  mapfile -t changed_files < <(git diff --name-only)
  test "${#changed_files[@]}" -eq 3
  test "${changed_files[0]}" = go.mod
  test "${changed_files[1]}" = go.sum
  test "${changed_files[2]}" = internal/storage/servers.json
)

mkdir -p "$output_dir/bin"
for architecture in amd64 arm64; do
  (
    cd "$source_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" GOWORK=off "$go_bin" build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -X main.version=${release_version}-waycloak -X main.created=1970-01-01T00:00:00Z -X main.commit=${upstream_commit}" \
      -o "$output_dir/bin/gluetun-entrypoint-linux-$architecture" \
      cmd/gluetun/main.go
  )
done

git -C "$source_dir" diff --binary -- go.mod go.sum >"$output_dir/gluetun-dependency.patch"
test -s "$output_dir/gluetun-dependency.patch"
cp "$source_dir/LICENSE" "$output_dir/LICENSE"
cp "$servers_source_dir/LICENSE" "$output_dir/gluetun-servers.LICENSE"
cp "$script_dir/../build/gluetun-candidate/control-auth.toml" "$output_dir/control-auth.toml"
test -s "$output_dir/control-auth.toml"
printf 'github.com/qdm12/gluetun-servers@%s\n' "$servers_commit" >"$output_dir/gluetun-servers.ref"
{
  sha256sum "$servers_source_dir/pkg/servers/protonvpn.json" | \
    sed 's|  .*/pkg/servers/protonvpn.json$|  pkg/servers/protonvpn.json|'
  sha256sum "$source_dir/internal/storage/servers.json" | \
    sed 's|  .*/internal/storage/servers.json$|  internal/storage/servers.json|'
} >"$output_dir/gluetun-servers.SHA256SUMS"
(
  cd "$output_dir"
  sha256sum bin/* >gluetun-binaries.SHA256SUMS
)
