// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package cniinstall

import "os"

// Persist directory entries before a later migration write can withdraw the
// previous chain. File fsync alone does not make rename durable across reboot.
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
