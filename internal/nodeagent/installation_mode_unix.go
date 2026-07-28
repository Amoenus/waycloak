// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !windows

package nodeagent

import "io/fs"

func protectedFileMode(mode fs.FileMode) bool {
	return mode&0o022 == 0
}
