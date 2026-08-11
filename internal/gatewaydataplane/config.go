// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"errors"
	"net/netip"
	"strings"
)

// DNSListenPort is the gateway overlay listener targeted by the workload
// data-plane redirect. Gluetun exclusively owns port 53 in the shared gateway
// network namespace.
const DNSListenPort uint16 = 1053

type Config struct {
	GatewayUID         string
	OverlayCIDR        netip.Prefix
	GatewayAddress     netip.Addr
	OverlayInterface   string
	UnderlayInterface  string
	TunnelInterface    string
	VXLANPort          uint16
	VNI                uint32
	MTU                int
	HealthPort         uint16
	DNSUpstream        netip.AddrPort
	ClusterDNSUpstream netip.AddrPort
	ClusterDomain      string
}

func (config Config) Validate() error {
	if config.GatewayUID == "" || !config.OverlayCIDR.IsValid() || !config.OverlayCIDR.Addr().Is4() || config.OverlayCIDR.Bits() < 16 || config.OverlayCIDR.Bits() > 29 ||
		!config.GatewayAddress.Is4() || !config.OverlayCIDR.Contains(config.GatewayAddress) || config.GatewayAddress == config.OverlayCIDR.Masked().Addr() ||
		config.OverlayInterface == "" || config.UnderlayInterface == "" || config.TunnelInterface == "" || config.OverlayInterface == config.TunnelInterface ||
		len(config.OverlayInterface) > 15 || len(config.UnderlayInterface) > 15 || len(config.TunnelInterface) > 15 || config.VXLANPort == 0 || config.HealthPort == 0 || config.VNI == 0 || config.VNI > 16777215 || config.MTU < 576 || config.MTU > 9000 ||
		!config.DNSUpstream.IsValid() || !config.DNSUpstream.Addr().IsLoopback() || config.DNSUpstream.Port() == 0 ||
		!config.ClusterDNSUpstream.IsValid() || !config.ClusterDNSUpstream.Addr().Is4() || config.ClusterDNSUpstream.Addr().IsLoopback() || config.ClusterDNSUpstream.Addr().IsUnspecified() || config.ClusterDNSUpstream.Port() != 53 || !validDomain(config.ClusterDomain) {
		return errors.New("exact gateway identity, IPv4 overlay, interfaces, VNI, MTU, health, and loopback DNS upstream are required")
	}
	return nil
}

func validDomain(value string) bool {
	if value == "" || len(value) > 253 || value[0] == '.' || value[len(value)-1] == '.' {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character < 'a' || character > 'z' {
				if character < '0' || character > '9' {
					if character != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}
