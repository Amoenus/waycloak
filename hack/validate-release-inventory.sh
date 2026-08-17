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

jq -e '
  .supportMatrix.rows == [{
    "id": "k3s-v1.36-flannel-containerd-linux-amd64-gluetun-proton-openvpn",
    "kubernetes": "v1.36.1+k3s1",
    "distribution": "k3s",
    "cni": "flannel",
    "runtime": "containerd://2.2.3-k3s1",
    "kernel": "linux>=5.10",
    "architecture": "amd64",
    "engine": "gluetun",
    "providerConfiguration": "protonvpn/openvpn",
    "features": [
      "networking.waycloak.io/DNS",
      "networking.waycloak.io/PortForwardServiceSingleActive",
      "networking.waycloak.io/TCP",
      "networking.waycloak.io/UDP",
      "networking.waycloak.io/WorkloadAdapter"
    ],
    "evidenceSuites": [
      "homelab-qbittorrent-proton",
      "k3s-datastore-recovery",
      "kind-turnkey-install"
    ]
  }]
' "$1" >/dev/null || {
  echo "release manifest must contain the exact certified support row" >&2
  exit 1
}
