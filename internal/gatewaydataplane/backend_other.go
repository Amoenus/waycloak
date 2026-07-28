// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package gatewaydataplane

import (
	"context"
	"errors"
)

type LinuxBackend struct{}

func (LinuxBackend) EnsureOverlay(context.Context, Config) error {
	return errors.New("gateway data plane requires Linux")
}
func (LinuxBackend) ReplaceRules(context.Context, Config, bool) error {
	return errors.New("gateway data plane requires Linux")
}
