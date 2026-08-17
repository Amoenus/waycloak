// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package gatewaycontract contains packet metadata shared by the gateway
// dataplane and optional gateway runtimes in one network namespace.
package gatewaycontract

// PortForwardIngressMark identifies packets that matched an exact active
// port-forward lease before DNAT. It is packet metadata, not a routable value.
const PortForwardIngressMark uint32 = 0x57434c50
