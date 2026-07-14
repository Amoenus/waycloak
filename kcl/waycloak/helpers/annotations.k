# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

gatewayAnnotation = "networking.waycloak.io/gateway"
portForwardContainerAnnotation = "networking.waycloak.io/port-forward-container"

schema GatewayReference:
    namespace: str
    name: str
    value: str = "${namespace}/${name}"

    check:
        len(namespace) > 0
        len(name) > 0
