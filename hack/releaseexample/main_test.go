// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"strings"
	"testing"
)

const testAdapter = "ghcr.io/amoenus/waycloak-qbittorrent-adapter@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestResolveAdapter(t *testing.T) {
	rendered, err := resolveAdapter([]byte("image: "+adapterPlaceholder+"\n"), testAdapter)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(rendered, []byte(testAdapter)) != 1 || bytes.Contains(rendered, []byte(adapterPlaceholder)) {
		t.Fatalf("unexpected rendered workload: %s", rendered)
	}
}

func TestResolveAdapterRejectsInvalidReference(t *testing.T) {
	_, err := resolveAdapter([]byte(adapterPlaceholder), "ghcr.io/amoenus/adapter:latest")
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveAdapterRequiresOnePlaceholder(t *testing.T) {
	for _, source := range []string{"", adapterPlaceholder + "\n" + adapterPlaceholder} {
		_, err := resolveAdapter([]byte(source), testAdapter)
		if err == nil || !strings.Contains(err.Error(), "exactly once") {
			t.Fatalf("unexpected error for %d placeholders: %v", strings.Count(source, adapterPlaceholder), err)
		}
	}
}
