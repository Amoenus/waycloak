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
  config/crd/bases/networking.waycloak.io_portforwardleases.yaml \
  config/crd/bases/networking.waycloak.io_vpnegressroutes.yaml \
  config/crd/bases/networking.waycloak.io_vpngatewayclasses.yaml \
  config/crd/bases/networking.waycloak.io_vpngateways.yaml \
  config/crd/bases/networking.waycloak.io_vpnworkloadbindings.yaml \
  config/crd/bases/networking.waycloak.io_workloadadapters.yaml \
  --output "$output" \
  --package waycloak

rm "$output/waycloak/kcl.mod"
kcl fmt "$output/waycloak/v1beta1" "$output/waycloak/k8s"
find "$output/waycloak" -type f -name '*.k' -exec perl -0pi -e 's/\n+\z/\n/' {} +
