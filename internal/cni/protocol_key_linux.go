// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package cni

import (
	"errors"
	"os"
	"syscall"
)

func validateProtocolKeyOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("local protocol key owner does not match the CNI process")
	}
	if os.Geteuid() != 0 {
		return errors.New("local protocol authentication requires a root CNI process")
	}
	return nil
}
