// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package dataplane

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
)

var ErrUnsupported = errors.New("waycloak data plane is unsupported on this platform")

type ClusterTrafficMode string

const (
	ClusterTrafficPreserve ClusterTrafficMode = "Preserve"
	ClusterTrafficGateway  ClusterTrafficMode = "Gateway"
	ClusterTrafficDeny     ClusterTrafficMode = "Deny"
)

type Config struct {
	PodUID                   string
	AllocationGeneration     int64
	GatewayGeneration        int64
	Address                  netip.Prefix
	OverlayCIDR              netip.Prefix
	GatewayAddress           netip.Addr
	GatewayEndpoint          netip.AddrPort
	GatewayHealthPort        uint16
	VNI                      uint32
	MTU                      int
	ClusterTrafficMode       ClusterTrafficMode
	ClusterCIDRs             []netip.Prefix
	UnderlayInterface        string
	OverlayInterfaceName     string
	ApplicationPortRedirects []ApplicationPortRedirect
}

type ApplicationPortRedirect struct {
	Identity        string
	TargetPort      uint16
	ApplicationPort uint16
	Protocols       []string
}

func (c Config) Validate() error {
	if c.PodUID == "" {
		return errors.New("pod UID is required")
	}
	if !c.Address.IsValid() || !c.OverlayCIDR.IsValid() || c.Address.Bits() != c.OverlayCIDR.Bits() || !c.OverlayCIDR.Contains(c.Address.Addr()) {
		return errors.New("allocated address must belong to the overlay CIDR")
	}
	if !c.GatewayAddress.IsValid() || !c.OverlayCIDR.Contains(c.GatewayAddress) || c.GatewayAddress == c.Address.Addr() {
		return errors.New("gateway address must be a different address in the overlay CIDR")
	}
	if !c.GatewayEndpoint.IsValid() || c.GatewayEndpoint.Port() == 0 {
		return errors.New("gateway underlay endpoint is required")
	}
	if c.GatewayHealthPort == 0 {
		return errors.New("observed gateway overlay health port is required")
	}
	if c.VNI == 0 || c.VNI > 16777215 {
		return errors.New("VNI must be between 1 and 16777215")
	}
	if c.MTU < 576 {
		return errors.New("MTU must be at least 576")
	}
	switch c.ClusterTrafficMode {
	case ClusterTrafficPreserve, ClusterTrafficGateway, ClusterTrafficDeny:
	default:
		return fmt.Errorf("unknown cluster traffic mode %q", c.ClusterTrafficMode)
	}
	for _, prefix := range c.ClusterCIDRs {
		if !prefix.IsValid() {
			return errors.New("cluster CIDRs must be valid")
		}
	}
	identities := map[string]struct{}{}
	matches := map[string]struct{}{}
	for _, redirect := range c.ApplicationPortRedirects {
		if redirect.Identity == "" || redirect.TargetPort == 0 || redirect.ApplicationPort == 0 || redirect.TargetPort == redirect.ApplicationPort || len(redirect.Protocols) == 0 {
			return errors.New("application port redirect is invalid")
		}
		if _, exists := identities[redirect.Identity]; exists {
			return errors.New("application port redirect identity is duplicated")
		}
		identities[redirect.Identity] = struct{}{}
		for _, protocol := range redirect.Protocols {
			if protocol != "TCP" && protocol != "UDP" {
				return errors.New("application port redirect protocol is invalid")
			}
			match := fmt.Sprintf("%d/%s", redirect.TargetPort, protocol)
			if _, exists := matches[match]; exists {
				return errors.New("application port redirect target is duplicated")
			}
			matches[match] = struct{}{}
		}
	}
	return nil
}

type Backend interface {
	Preflight(context.Context) error
	InstallLockdown(context.Context, string) error
	Configure(context.Context, Config) error
	Verify(context.Context, Config) error
	Repair(context.Context, Config) error
}

type Agent struct{ Backend Backend }

func (a Agent) Prepare(ctx context.Context, cfg Config) error {
	if a.Backend == nil {
		return errors.New("data-plane backend is required")
	}
	if cfg.PodUID == "" {
		return errors.New("pod UID is required before lockdown")
	}
	if err := a.Backend.InstallLockdown(ctx, cfg.PodUID); err != nil {
		return fmt.Errorf("install fail-closed policy: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate protected-path configuration after lockdown: %w", err)
	}
	if err := a.Backend.Configure(ctx, cfg); err != nil {
		return fmt.Errorf("configure protected path with lockdown retained: %w", err)
	}
	return nil
}

func (a Agent) Verify(ctx context.Context, cfg Config) error {
	if a.Backend == nil {
		return errors.New("data-plane backend is required")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return a.Backend.Verify(ctx, cfg)
}

func (a Agent) Repair(ctx context.Context, cfg Config) error {
	if a.Backend == nil {
		return errors.New("data-plane backend is required")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return a.Backend.Repair(ctx, cfg)
}
