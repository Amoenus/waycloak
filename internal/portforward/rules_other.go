// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package portforward

import (
	"context"
	"errors"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

type LinuxRuleBackend struct {
	TunnelInterface  string
	OverlayInterface string
}

func (LinuxRuleBackend) Replace(context.Context, []GatewayRule) error {
	return errors.New("gateway nftables rules require Linux")
}

func (LinuxRuleBackend) Ready(context.Context, GatewayRule) (bool, error) {
	return false, errors.New("gateway nftables rules require Linux")
}

func (LinuxRuleBackend) Withdrawn(context.Context, wayv1.ObjectUID, int64) (bool, error) {
	return false, errors.New("gateway nftables rules require Linux")
}

var _ RuleBackend = LinuxRuleBackend{}
