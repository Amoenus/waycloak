# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

import helpers
import v1alpha1 as networking

_gateway_ref = helpers.GatewayReference {
    namespace = "private-egress"
    name = "proton-eu"
}

_gateway = networking.VPNGateway {
    metadata = {
        name = _gateway_ref.name
        namespace = _gateway_ref.namespace
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
            credentialsSecretRef.name = "protonvpn-credentials"
        }
        overlay = {
            cidr = "172.30.99.0/24"
            vni = 7999
            mtu = 1320
        }
        dns.mode = "Gateway"
        clusterTraffic.mode = "Preserve"
        portForwarding = {
            enabled = True
            driver = "ProtonNatPmp"
        }
        workloadAccess.namespaceSelector.matchLabels = {
            "networking.waycloak.io/access" = "allowed"
        }
    }
}

_lease = networking.PortForwardLease {
    metadata = {
        name = "torrent"
        namespace = "applications"
    }
    spec = {
        gatewayRef = {
            namespace = _gateway_ref.namespace
            name = _gateway_ref.name
        }
        target = {
            podSelector.matchLabels = {
                "app.kubernetes.io/name" = "qbittorrent"
            }
            port = 6881
            applicationPortMode = "ProviderAssigned"
        }
        protocols = ["TCP", "UDP"]
    }
}

items = [_gateway, _lease]
