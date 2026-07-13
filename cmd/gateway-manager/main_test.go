// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineForRejectsUnsupportedTypes(t *testing.T) {
	if _, err := engineFor("FakeVPN", "", ""); err == nil {
		t.Fatal("unsupported engine was accepted")
	}
	if _, err := engineFor("Gluetun", "http://127.0.0.1:9999/", "http://127.0.0.1:8000"); err != nil {
		t.Fatal(err)
	}
}

func TestRenderEngineFirewallCommand(t *testing.T) {
	directory := t.TempDir()
	basePath := filepath.Join(directory, "base.txt")
	resolvPath := filepath.Join(directory, "resolv.conf")
	outputPath := filepath.Join(directory, "post-rules.txt")
	resolverOutputPath := filepath.Join(directory, "captured-resolv.conf")
	if err := os.WriteFile(basePath, []byte("iptables --policy FORWARD ACCEPT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolvPath, []byte("nameserver 10.43.0.10\nsearch apps.svc.cluster.local svc.cluster.local cluster.local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"render-engine-firewall", "--base-path=" + basePath, "--resolv-conf=" + resolvPath, "--output=" + outputPath, "--resolver-output=" + resolverOutputPath}); err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "--destination 10.43.0.10/32 --protocol udp --destination-port 53 --jump ACCEPT") {
		t.Fatalf("rendered engine firewall = %s", rendered)
	}
	captured, err := os.ReadFile(resolverOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(captured) != "nameserver 10.43.0.10\nsearch apps.svc.cluster.local. svc.cluster.local. cluster.local.\n" {
		t.Fatalf("captured resolver configuration = %q", captured)
	}
}
