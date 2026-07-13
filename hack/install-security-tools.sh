#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

destination="${1:-${RUNNER_TEMP:-/tmp}/waycloak-security-tools}"
mkdir -p "$destination"

download_and_extract() {
  local url="$1"
  local checksum="$2"
  local archive="$3"
  curl --fail --silent --show-error --location --output "$archive" "$url"
  printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check --status
  tar --extract --gzip --file "$archive" --directory "$destination"
}

download_and_extract \
  "https://github.com/aquasecurity/trivy/releases/download/v0.70.0/trivy_0.70.0_Linux-64bit.tar.gz" \
  "8b4376d5d6befe5c24d503f10ff136d9e0c49f9127a4279fd110b727929a5aa9" \
  "$destination/trivy.tar.gz"

download_and_extract \
  "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_x64.tar.gz" \
  "551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb" \
  "$destination/gitleaks.tar.gz"

rm -f "$destination/trivy.tar.gz" "$destination/gitleaks.tar.gz"
"$destination/trivy" --version
"$destination/gitleaks" version

if [[ -n "${GITHUB_PATH:-}" ]]; then
  printf '%s\n' "$destination" >>"$GITHUB_PATH"
else
  printf '%s\n' "$destination"
fi
