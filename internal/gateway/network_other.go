// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package gateway

import "context"

type unsupportedNetwork struct{}

func NewNetwork() Network { return unsupportedNetwork{} }

func (unsupportedNetwork) Reconcile(context.Context, DesiredState) error {
	return ErrNetworkUnsupported
}
