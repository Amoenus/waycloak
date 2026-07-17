// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallExecutableCopiesAtomically(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	target := filepath.Join(directory, "bin", "bitmagnet-adapter")
	if err := os.WriteFile(source, []byte("adapter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installExecutable(source, target); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || string(installed) != "adapter" {
		t.Fatalf("installed=%q err=%v", installed, err)
	}
	info, err := os.Stat(target)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}
