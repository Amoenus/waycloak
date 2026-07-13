// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
)

var ErrForwardingUnsupported = errors.New("gateway forwarding is unsupported on this platform")

type Forwarding interface {
	InstallLockdown(context.Context, DesiredState) error
	Reconcile(context.Context, DesiredState) error
	ObservePortForwardRules(context.Context, DesiredState) ([]PortForwardRuleObservation, error)
}

type PortForwardRuleObservation struct {
	Identity        string
	LeaseGeneration int64
	TargetAddress   string
	TargetPort      uint16
	Ready           bool
}
