// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build windows

package nodeagent

import "io/fs"

// Windows does not expose Unix group/world permission bits through FileMode.
// The production node agent is Linux-only; portable tests still verify exact
// identity, content digests, regular-file handling, and symlink rejection.
func protectedFileMode(fs.FileMode) bool {
	return true
}
