// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !windows

package qbittorrentadapter

import "os"

func replaceFile(source, target string) error { return os.Rename(source, target) }
