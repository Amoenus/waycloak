# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

import helpers
import v1alpha1 as networking

gateway_ref = helpers.GatewayReference {
    namespace = "waycloak-egress"
    name = "proton-eu"
}

gateway = networking.VPNGateway {
    metadata = {
        name = gateway_ref.name
        namespace = gateway_ref.namespace
    }
    spec = {
        engine = {
            $type = "Gluetun"
            image = "docker.io/qmcgaw/gluetun:v3.41.0@sha256:6b54856716d0de56e5bb00a77029b0adea57284cf5a466f23aad5979257d3045"
        }
        provider = {
            name = "protonvpn"
            $protocol = "openvpn"
            region = "Netherlands"
            credentialsSecretRef.name = "vpn-credentials"
        }
        overlay = {
            cidr = "172.30.99.0/24"
            vni = 7999
            mtu = 1320
        }
        dns.mode = "Gateway"
        clusterTraffic = {
            mode = "Preserve"
            cidrs = ["10.42.0.0/16", "10.43.0.0/16"]
        }
        portForwarding.enabled = False
        workloadAccess.namespaceSelector.matchLabels = {
            "networking.waycloak.io/access" = "allowed"
        }
    }
}

# Merge this map into the Pod template of each workload that should use the
# gateway. Waycloak otherwise remains invisible to the application.
pod_template_annotations = {
    helpers.gatewayAnnotation = gateway_ref.value
}

items = [gateway]
