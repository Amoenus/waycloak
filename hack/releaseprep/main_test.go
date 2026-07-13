// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparePinsReleaseChart(t *testing.T) {
	directory := t.TempDir()
	mustWrite(t, filepath.Join(directory, "Chart.yaml"), "version: 0.1.0-alpha.1\nappVersion: 0.1.0-alpha.1\n")
	mustWrite(t, filepath.Join(directory, "values.yaml"), `images:
  controller:
    repository: old/controller
    digest: ""
  agent:
    repository: old/agent
    digest: ""
  gatewayManager:
    repository: old/manager
    digest: ""
`)
	digest := "sha256:" + strings.Repeat("a", 64)
	images := map[string]string{"controller": "ghcr.io/example/controller@" + digest, "agent": "ghcr.io/example/agent@" + digest, "gatewayManager": "ghcr.io/example/manager@" + digest}
	if err := prepare(directory, "0.1.0", images); err != nil {
		t.Fatal(err)
	}
	chart, err := os.ReadFile(filepath.Join(directory, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(chart) != "version: 0.1.0\nappVersion: 0.1.0\n" {
		t.Fatalf("Chart.yaml = %s", chart)
	}
	values, err := os.ReadFile(filepath.Join(directory, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"repository: ghcr.io/example/controller", "repository: ghcr.io/example/agent", "repository: ghcr.io/example/manager", `digest: "` + digest + `"`} {
		if !strings.Contains(string(values), wanted) {
			t.Fatalf("values.yaml does not contain %q: %s", wanted, values)
		}
	}
}

func TestSplitDigestReferenceRejectsMutableReference(t *testing.T) {
	if _, _, err := splitDigestReference("ghcr.io/example/controller:latest"); err == nil {
		t.Fatal("mutable image reference was accepted")
	}
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
