#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

version="0.12.4"
checksum="01dc1d8914ed8fdfac7e8b34cc155227cf7f155ef8760f741f35b0f8529f08ca"
destination="${1:-${RUNNER_TEMP:-/tmp}/waycloak-kcl}"
archive="$destination/kcl.tar.gz"

mkdir -p "$destination"
curl --fail --silent --show-error --location \
  --output "$archive" \
  "https://github.com/kcl-lang/cli/releases/download/v${version}/kcl-v${version}-linux-amd64.tar.gz"
printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check --status
tar --extract --gzip --file "$archive" --directory "$destination"
rm -f "$archive"

kcl_path="$(find "$destination" -type f -name kcl -print -quit)"
test -n "$kcl_path"
chmod +x "$kcl_path"
if [[ "$kcl_path" != "$destination/kcl" ]]; then
  ln -sf "$kcl_path" "$destination/kcl"
fi
"$destination/kcl" version

if [[ -n "${GITHUB_PATH:-}" ]]; then
  printf '%s\n' "$destination" >>"$GITHUB_PATH"
else
  printf '%s\n' "$destination"
fi
