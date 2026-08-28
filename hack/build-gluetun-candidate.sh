#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

if [[ "$#" -ne 4 ]]; then
  echo "usage: build-gluetun-candidate.sh SOURCE_DIR OUTPUT_DIR UPSTREAM_COMMIT RELEASE_VERSION" >&2
  exit 2
fi

source_dir="$1"
output_dir="$2"
upstream_commit="$3"
release_version="$4"
go_bin="${GO:-go}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! "$upstream_commit" =~ ^[a-f0-9]{40}$ ]]; then
  echo "upstream commit must be one exact lowercase Git commit" >&2
  exit 1
fi
bash "$script_dir/validate-release-tag.sh" "$release_version"
if [[ "$(git -C "$source_dir" rev-parse HEAD)" != "$upstream_commit" ]] || \
  [[ -n "$(git -C "$source_dir" status --porcelain)" ]]; then
  echo "Gluetun source must be the clean exact upstream commit" >&2
  exit 1
fi

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
  test "${#changed_files[@]}" -eq 2
  test "${changed_files[0]}" = go.mod
  test "${changed_files[1]}" = go.sum
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
cp "$script_dir/../build/gluetun-candidate/control-auth.toml" "$output_dir/control-auth.toml"
test -s "$output_dir/control-auth.toml"
(
  cd "$output_dir"
  sha256sum bin/* >gluetun-binaries.SHA256SUMS
)
