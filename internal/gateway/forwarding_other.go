// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package gateway

import "context"

type unsupportedForwarding struct{}

func NewForwarding() Forwarding { return unsupportedForwarding{} }

func (unsupportedForwarding) InstallLockdown(context.Context, DesiredState) error {
	return ErrForwardingUnsupported
}

func (unsupportedForwarding) Reconcile(context.Context, DesiredState) error {
	return ErrForwardingUnsupported
}

func (unsupportedForwarding) ObservePortForwardRules(context.Context, DesiredState) ([]PortForwardRuleObservation, error) {
	return nil, ErrForwardingUnsupported
}
