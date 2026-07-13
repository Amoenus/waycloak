// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQBittorrentExampleRendersProviderAssignedAdapter(t *testing.T) {
	example := filepath.Join("..", "..", "examples", "qbittorrent")
	rendered := command(t, nil, "kubectl", "kustomize", example)
	for _, required := range []string{
		"applicationPortMode: ProviderAssigned",
		"name: waycloak-qbittorrent-adapter",
		"automountServiceAccountToken: false",
		"drop:\n            - ALL",
		"WAYCLOAK_QBITTORRENT_API_KEY_FILE",
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("rendered qBitTorrent example does not contain %q", required)
		}
	}
	if strings.Contains(rendered, ":latest") {
		t.Fatal("rendered qBitTorrent example contains a mutable latest image")
	}
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "image:") && !strings.Contains(trimmed, "@sha256:") {
			t.Fatalf("rendered qBitTorrent example contains a mutable image: %s", trimmed)
		}
	}
}
