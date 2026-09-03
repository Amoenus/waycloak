# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

# Cluster-owned override for the release-generated Helm values. Review this
# CIDR against Pod, Service, node, LAN, and VPN networks before committing it.
values = {
    controller.gateway.overlayCIDR = "100.96.0.0/16"
}
