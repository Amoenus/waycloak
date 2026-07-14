#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

output="${1:?usage: generate-kcl-models.sh OUTPUT_DIRECTORY}"
test ! -e "$output" || {
  echo "output path already exists: $output" >&2
  exit 1
}

kcl import --mode crd \
  config/crd/bases/networking.waycloak.io_vpngateways.yaml \
  config/crd/bases/networking.waycloak.io_vpnworkloads.yaml \
  config/crd/bases/networking.waycloak.io_portforwardleases.yaml \
  --output "$output" \
  --package waycloak

rm "$output/waycloak/kcl.mod"
find "$output/waycloak" -type f -name '*.k' -exec perl -0pi -e 's/\n+\z/\n/' {} +
