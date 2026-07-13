// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package dataplane

import "context"

type unsupportedBackend struct{}

func NewBackend() Backend { return unsupportedBackend{} }

func (unsupportedBackend) Preflight(context.Context) error               { return ErrUnsupported }
func (unsupportedBackend) InstallLockdown(context.Context, string) error { return ErrUnsupported }
func (unsupportedBackend) Configure(context.Context, Config) error       { return ErrUnsupported }
func (unsupportedBackend) Verify(context.Context, Config) error          { return ErrUnsupported }
func (unsupportedBackend) Repair(context.Context, Config) error          { return ErrUnsupported }
