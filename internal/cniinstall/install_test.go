// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cniinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

func TestInstallIsAtomicIdempotentAndReceiptBacked(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	config := filepath.Join(directory, "10-primary.conflist")
	write(t, source, "binary", 0o755)
	original := "{\"cniVersion\":\"1.1.0\",\"name\":\"primary\",\"plugins\":[{\"type\":\"bridge\"}]}\n"
	write(t, config, original, 0o644)
	options := fixture(directory, source, config)
	if err := Install(options); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(config)
	if err != nil || strings.Count(string(first), `"type": "waycloak-cni"`) != 1 {
		t.Fatalf("Waycloak chain was not installed exactly once: %v %s", err, first)
	}
	if backup, err := os.ReadFile(options.BackupPath); err != nil || string(backup) != original {
		t.Fatalf("original chain was not preserved: %v %s", err, backup)
	}
	if err := Install(options); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(config)
	if string(first) != string(second) {
		t.Fatal("idempotent install changed the CNI config")
	}
}

func TestInstallRefusesForeignExistingEntryAndPreservesConfig(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	config := filepath.Join(directory, "10-primary.conflist")
	write(t, source, "binary", 0o755)
	original := `{"cniVersion":"1.1.0","name":"primary","plugins":[{"type":"bridge"},{"type":"waycloak-cni","agentSocket":"/foreign"}]}`
	write(t, config, original, 0o644)
	if err := Install(fixture(directory, source, config)); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("foreign chain was not rejected: %v", err)
	}
	current, _ := os.ReadFile(config)
	if string(current) != original {
		t.Fatal("rejected install modified the primary chain")
	}
}

func TestInstallRefusesAdoptionWithoutOriginalBackup(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	config := filepath.Join(directory, "10-primary.conflist")
	write(t, source, "binary", 0o755)
	write(t, config, `{"cniVersion":"1.1.0","name":"primary","plugins":[{"type":"bridge"},{"type":"waycloak-cni","agentSocket":"/run/waycloak/agent.sock","agentKeyFile":"/run/waycloak/agent.key","stateDir":"/var/lib/cni/waycloak"}]}`, 0o644)
	if err := Install(fixture(directory, source, config)); err == nil || !strings.Contains(err.Error(), "preserved") {
		t.Fatalf("unrecoverable pre-existing chain was adopted: %v", err)
	}
}

func fixture(directory, source, config string) Options {
	return Options{
		SourceBinary: source, BinaryPath: filepath.Join(directory, "bin", "waycloak-cni"), ConfigPath: config,
		ReceiptPath: filepath.Join(directory, "state", "install-receipt.json"), BackupPath: config + ".waycloak-original",
		AgentSocket: "/run/waycloak/agent.sock", AgentKeyFile: "/run/waycloak/agent.key", StateDirectory: "/var/lib/cni/waycloak",
		ReleaseIdentity: wayv1.ReleaseIdentity{Version: "v1.0.0-beta.1", ManifestDigest: "sha256:" + strings.Repeat("a", 64)},
	}
}

func write(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}
