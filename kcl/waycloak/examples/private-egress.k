# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT
import v1beta1 as networking

_route = networking.VPNEgressRoute {
    metadata = {
        name = "private"
        namespace = "media"
    }
    spec = {
        parentRefs = [{
            group = "networking.waycloak.io"
            kind = "VPNGateway"
            namespace = "media"
            name = "private"
        }]
        requiredFeatures = [
            "networking.waycloak.io/TCP"
            "networking.waycloak.io/UDP"
            "networking.waycloak.io/DNSContainment"
        ]
    }
}

_workload = {
    apiVersion = "apps/v1"
    kind = "Deployment"
    metadata = {
        name = "curl"
        namespace = "media"
    }
    spec = {
        replicas = 1
        selector.matchLabels.app = "curl"
        template = {
            metadata = {
                labels = {
                    app = "curl"
                    "networking.waycloak.io/egress-route" = _route.metadata.name
                }
            }
            spec = {
                automountServiceAccountToken = False
                containers = [{
                    name = "curl"
                    image = "docker.io/curlimages/curl:8.14.1@sha256:1111111111111111111111111111111111111111111111111111111111111111"
                    command = ["sleep", "infinity"]
                }]
            }
        }
    }
}

items = [_route, _workload]
