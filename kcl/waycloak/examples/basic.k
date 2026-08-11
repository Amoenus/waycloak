# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT
import v1beta1 as networking

_core_features = [
    "networking.waycloak.io/FailClosedEgress"
    "networking.waycloak.io/TCP"
    "networking.waycloak.io/UDP"
    "networking.waycloak.io/DNSContainment"
    "networking.waycloak.io/GatewayReplacementRecovery"
    "networking.waycloak.io/NodeRestartRecovery"
]

_gateway_class = networking.VPNGatewayClass {
    metadata.name = "example.waycloak.io"
    spec = {
        controllerName = "example.waycloak.io/controller"
        releaseIdentity = {
            version = "v1.0.0-beta.1"
            manifestDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
        }
        supportedFeatures = _core_features
        conformanceProfile = "networking.waycloak.io/Core-v1"
    }
}

_gateway = networking.VPNGateway {
    metadata = {
        name = "private"
        namespace = "media"
    }
    spec = {
        gatewayClassName = _gateway_class.metadata.name
        requestedFeatures = _core_features
        allowedRoutes.namespaces.from = "Same"
        clusterTraffic.mode = "TunnelAll"
        dns.mode = "Gateway"
    }
}

items = [_gateway_class, _gateway]
