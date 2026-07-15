# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

import helpers
import v1alpha1 as networking

gateway_ref = helpers.GatewayReference {
    namespace = "waycloak-egress"
    name = "private"
}

engine_config = {
    apiVersion = "v1"
    kind = "ConfigMap"
    metadata = {
        name = "gluetun-native"
        namespace = gateway_ref.namespace
    }
    data = {
        VPN_SERVICE_PROVIDER = "custom"
        VPN_TYPE = "wireguard"
    }
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
            config = {
                envFrom = [{name = engine_config.metadata.name}]
                # Provision this Secret separately with a native wg0.conf key.
                files = [{
                    secretRef.name = "wireguard-config"
                    mountPath = "/gluetun/wireguard"
                }]
            }
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

items = [engine_config, gateway]
