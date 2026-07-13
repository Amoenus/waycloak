// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
)

type DesiredSource interface {
	Load() (DesiredState, error)
}

type FileSource struct{ Path string }

func (source FileSource) Load() (DesiredState, error) {
	data, err := os.ReadFile(source.Path)
	if err != nil {
		return DesiredState{}, fmt.Errorf("read gateway desired state: %w", err)
	}
	var desired DesiredState
	if err := json.Unmarshal(data, &desired); err != nil {
		return DesiredState{}, fmt.Errorf("decode gateway desired state: %w", err)
	}
	if err := desired.Validate(); err != nil {
		return DesiredState{}, err
	}
	return desired, nil
}

func (desired DesiredState) Validate() error {
	prefix, err := netip.ParsePrefix(desired.OverlayCIDR)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() > 30 {
		return errors.New("gateway overlay CIDR is invalid")
	}
	gatewayAddress, err := netip.ParseAddr(desired.GatewayAddress)
	if err != nil || !prefix.Contains(gatewayAddress) {
		return errors.New("gateway overlay address is invalid")
	}
	if desired.VNI < 1 || desired.VNI > 16777215 || desired.MTU < 576 || desired.VXLANPort < 1 || desired.VXLANPort > 65535 {
		return errors.New("gateway VXLAN settings are invalid")
	}
	identities := map[string]struct{}{}
	addresses := map[netip.Addr]struct{}{gatewayAddress: {}}
	for _, member := range desired.Members {
		address, addressErr := netip.ParseAddr(member.OverlayAddress)
		underlay, underlayErr := netip.ParseAddr(member.UnderlayIP)
		if member.ID == "" || addressErr != nil || !prefix.Contains(address) || underlayErr != nil || !underlay.IsValid() {
			return errors.New("gateway member is invalid")
		}
		if _, exists := identities[member.ID]; exists {
			return errors.New("gateway member identity is duplicated")
		}
		if _, exists := addresses[address]; exists {
			return errors.New("gateway member overlay address is duplicated")
		}
		identities[member.ID] = struct{}{}
		addresses[address] = struct{}{}
	}
	return nil
}
