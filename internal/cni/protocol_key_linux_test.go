// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package cni

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProtocolKeyRejectsUnsafeFilesystemBoundary(t *testing.T) {
	t.Run("directory mode", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "agent.key")
		writeProtocolTestKey(t, path, 0o600)
		if _, err := LoadProtocolKey(path); err == nil || !strings.Contains(err.Error(), "directory permissions") {
			t.Fatalf("directory mode error = %v", err)
		}
	})

	t.Run("directory symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(link, "agent.key")
		writeProtocolTestKey(t, filepath.Join(target, "agent.key"), 0o600)
		if _, err := LoadProtocolKey(path); err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
			t.Fatalf("directory symlink error = %v", err)
		}
	})

	t.Run("key symlink", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.key")
		writeProtocolTestKey(t, target, 0o600)
		link := filepath.Join(directory, "agent.key")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProtocolKey(link); err == nil || !strings.Contains(err.Error(), "non-symlink file") {
			t.Fatalf("key symlink error = %v", err)
		}
	})

	t.Run("key mode", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "agent.key")
		writeProtocolTestKey(t, path, 0o644)
		if _, err := LoadProtocolKey(path); err == nil || !strings.Contains(err.Error(), "key permissions") {
			t.Fatalf("key mode error = %v", err)
		}
	})
}

func writeProtocolTestKey(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x7a}, ProtocolKeySize), mode); err != nil {
		t.Fatal(err)
	}
}
