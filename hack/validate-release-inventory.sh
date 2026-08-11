#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

if [[ "$#" -ne 1 ]] || [[ ! -f "$1" ]]; then
  echo "usage: validate-release-inventory.sh RELEASE_MANIFEST" >&2
  exit 2
fi

readonly expected='["gluetun","pause","replacement-controller","waycloak-cni","waycloak-gateway-agent","waycloak-gateway-runtime","waycloak-node-agent","waycloak-qbittorrent-adapter"]'
actual="$(jq -ce '.images | keys | sort' "$1")"
if [[ "$actual" != "$expected" ]]; then
  echo "release manifest must contain exactly the complete eight-image Waycloak inventory" >&2
  exit 1
fi
