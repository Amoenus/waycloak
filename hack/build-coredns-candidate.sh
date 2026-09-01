#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

if [[ "$#" -ne 6 ]]; then
  echo "usage: build-coredns-candidate.sh SOURCE_DIR OUTPUT_DIR UPSTREAM_COMMIT X_CRYPTO_VERSION X_MOD_VERSION RELEASE_TAG" >&2
  exit 2
fi

readonly source_dir="$1"
readonly output_dir="$2"
readonly upstream_commit="$3"
readonly x_crypto_version="$4"
readonly x_mod_version="$5"
readonly release_tag="$6"

test "$(git -C "$source_dir" rev-parse HEAD)" = "$upstream_commit"
mkdir -p "$output_dir"

(
  cd "$source_dir"
  export GOWORK=off
  go get "golang.org/x/crypto@${x_crypto_version}"
  go get "golang.org/x/mod@${x_mod_version}"
  test "$(go list -m -f '{{.Version}}' golang.org/x/crypto)" = "$x_crypto_version"
  test "$(go list -m -f '{{.Version}}' golang.org/x/mod)" = "$x_mod_version"
  git diff --exit-code -- coredns.go plugin.cfg
  git diff --binary -- go.mod go.sum >"$output_dir/coredns-dependency.patch"
  test -s "$output_dir/coredns-dependency.patch"

  for architecture in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
      go build -trimpath -tags=grpcnotrace \
      -ldflags="-s -w -buildid= -X github.com/coredns/coredns/coremain.GitCommit=${upstream_commit}-waycloak-${release_tag#v}" \
      -o "$output_dir/coredns-${architecture}" .
  done
)

cp "$source_dir/LICENSE" "$output_dir/coredns.LICENSE"
printf 'github.com/coredns/coredns@%s\n' "$upstream_commit" >"$output_dir/coredns-source.ref"
(
  cd "$output_dir"
  sha256sum coredns-amd64 coredns-arm64 >coredns-binaries.SHA256SUMS
)
