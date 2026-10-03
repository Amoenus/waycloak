// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package cniinstall

// Non-Linux builds support host-side format tests only; Waycloak's CNI installer
// runs exclusively on Linux, where directory fsync is required.
func syncDirectory(string) error { return nil }
