# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT
import v1beta1 as networking

_adapter = networking.WorkloadAdapter {
    metadata = {
        name = "qbittorrent"
        namespace = "media"
    }
    spec = {
        protocolVersion = "networking.waycloak.io/adapter/v1"
        image = "ghcr.io/example/waycloak-qbittorrent-adapter@sha256:1111111111111111111111111111111111111111111111111111111111111111"
        supportedApplications = ["networking.waycloak.io/qbittorrent"]
        supportedFeatures = ["networking.waycloak.io/PortForwardServiceSingleActive"]
    }
}

items = [_adapter]
