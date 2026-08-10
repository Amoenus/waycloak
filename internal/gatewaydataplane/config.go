// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"errors"
	"net/netip"
)

// DNSListenPort is the gateway overlay listener targeted by the workload
// data-plane redirect. Gluetun exclusively owns port 53 in the shared gateway
// network namespace.
const DNSListenPort uint16 = 1053

type Config struct {
	GatewayUID        string
	OverlayCIDR       netip.Prefix
	GatewayAddress    netip.Addr
	OverlayInterface  string
	UnderlayInterface string
	TunnelInterface   string
	VXLANPort         uint16
	VNI               uint32
	MTU               int
	HealthPort        uint16
	DNSUpstream       netip.AddrPort
}

func (config Config) Validate() error {
	if config.GatewayUID == "" || !config.OverlayCIDR.IsValid() || !config.OverlayCIDR.Addr().Is4() || config.OverlayCIDR.Bits() < 16 || config.OverlayCIDR.Bits() > 29 ||
		!config.GatewayAddress.Is4() || !config.OverlayCIDR.Contains(config.GatewayAddress) || config.GatewayAddress == config.OverlayCIDR.Masked().Addr() ||
		config.OverlayInterface == "" || config.UnderlayInterface == "" || config.TunnelInterface == "" || config.OverlayInterface == config.TunnelInterface ||
		len(config.OverlayInterface) > 15 || len(config.UnderlayInterface) > 15 || len(config.TunnelInterface) > 15 || config.VXLANPort == 0 || config.HealthPort == 0 || config.VNI == 0 || config.VNI > 16777215 || config.MTU < 576 || config.MTU > 9000 ||
		!config.DNSUpstream.IsValid() || !config.DNSUpstream.Addr().IsLoopback() || config.DNSUpstream.Port() == 0 {
		return errors.New("exact gateway identity, IPv4 overlay, interfaces, VNI, MTU, health, and loopback DNS upstream are required")
	}
	return nil
}
