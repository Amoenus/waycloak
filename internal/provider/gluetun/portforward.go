// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Amoenus/waycloak/internal/provider"
	"github.com/Amoenus/waycloak/internal/provider/proton"
)

// PortForwardCapabilityProtonNATPMP identifies the first port-forward
// capability supplied through the Gluetun engine adapter. It does not make
// Waycloak a Proton VPN engine: Gluetun still establishes and owns the tunnel,
// while this narrow implementation manages Proton's NAT-PMP lease through it.
const PortForwardCapabilityProtonNATPMP = "gluetun.waycloak.io/proton-natpmp"

type PortForwardOptions struct {
	CapabilityName  string
	TunnelInterface string
}

// PortForwardCapabilityForConfig selects a capability from Gluetun-native
// configuration. Desired feature names never infer support on their own.
func PortForwardCapabilityForConfig(config map[string]string) (string, error) {
	providerName := strings.ToLower(strings.TrimSpace(config["VPN_SERVICE_PROVIDER"]))
	vpnType := strings.ToLower(strings.TrimSpace(config["VPN_TYPE"]))
	if providerName == "protonvpn" && vpnType == "openvpn" {
		return PortForwardCapabilityProtonNATPMP, nil
	}
	return "", fmt.Errorf("gluetun native configuration does not expose a supported port-forward capability (provider=%q, vpnType=%q)", providerName, vpnType)
}

// NewPortForwardCapability constructs only implementations owned by the
// Gluetun engine adapter. The generic gateway runtime never imports a concrete
// provider implementation.
func NewPortForwardCapability(options PortForwardOptions) (provider.PortForwardCapability, error) {
	if options.TunnelInterface == "" {
		return nil, errors.New("gluetun port-forward capability requires a tunnel interface")
	}
	switch options.CapabilityName {
	case PortForwardCapabilityProtonNATPMP:
		return proton.New(options.TunnelInterface), nil
	default:
		return nil, fmt.Errorf("unsupported Gluetun port-forward capability %q", options.CapabilityName)
	}
}

var _ provider.PortForwardCapability = (*proton.Client)(nil)
