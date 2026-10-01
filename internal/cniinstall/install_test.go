// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cniinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/nodeagent"
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
	write(t, config, `{"cniVersion":"1.1.0","name":"primary","plugins":[{"type":"bridge"},{"type":"waycloak-cni","agentSocket":"/run/waycloak/agent.sock","agentKeyFile":"/run/waycloak/agent.key","stateDir":"/var/lib/cni/waycloak/attachments"}]}`, 0o644)
	if err := Install(fixture(directory, source, config)); err == nil || !strings.Contains(err.Error(), "preserved") {
		t.Fatalf("unrecoverable pre-existing chain was adopted: %v", err)
	}
}

func TestInstallReinstallsWhenPreservedOriginalMatchesActiveUnchainedConfig(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	config := filepath.Join(directory, "10-primary.conflist")
	write(t, source, "binary-v1", 0o755)
	original := "{\"cniVersion\":\"1.1.0\",\"name\":\"primary\",\"plugins\":[{\"type\":\"bridge\"}]}\n"
	write(t, config, original, 0o644)
	options := fixture(directory, source, config)
	write(t, options.BackupPath, original, 0o600)
	if err := Install(options); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(config)
	if err != nil || strings.Count(string(installed), `"type": "waycloak-cni"`) != 1 {
		t.Fatalf("Waycloak chain was not reinstalled exactly once: %v %s", err, installed)
	}
	backup, err := os.ReadFile(options.BackupPath)
	if err != nil || string(backup) != original {
		t.Fatalf("matching preserved original changed: %v %s", err, backup)
	}
}

func TestInstallRefusesMismatchedPreservedOriginalForUnchainedConfig(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	config := filepath.Join(directory, "10-primary.conflist")
	write(t, source, "binary-v1", 0o755)
	original := "{\"cniVersion\":\"1.1.0\",\"name\":\"primary\",\"plugins\":[{\"type\":\"bridge\"}]}\n"
	write(t, config, original, 0o644)
	options := fixture(directory, source, config)
	write(t, options.BackupPath, strings.Replace(original, "bridge", "flannel", 1), 0o600)
	if err := Install(options); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("mismatched preserved original was accepted: %v", err)
	}
	current, _ := os.ReadFile(config)
	if string(current) != original {
		t.Fatal("rejected reinstall modified the active config")
	}
}

func TestInstallRefusesReceiptInAttachmentStateDirectory(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	config := filepath.Join(directory, "10-primary.conflist")
	write(t, source, "binary", 0o755)
	write(t, config, `{"cniVersion":"1.1.0","name":"primary","plugins":[{"type":"bridge"}]}`, 0o644)
	options := fixture(directory, source, config)
	options.StateDirectory = filepath.ToSlash(filepath.Dir(options.ReceiptPath))
	if !strings.HasPrefix(options.StateDirectory, "/") {
		options.StateDirectory = "/" + options.StateDirectory
	}
	if err := Install(options); err == nil || !strings.Contains(err.Error(), "distinct from the installation receipt") {
		t.Fatalf("colliding receipt and attachment state directories were not rejected: %v", err)
	}
}

func fixture(directory, source, config string) Options {
	return Options{
		SourceBinary: source, BinaryPath: filepath.Join(directory, "bin", "waycloak-cni"), ConfigPath: config,
		ReceiptPath: filepath.Join(directory, "state", "install-receipt.json"), BackupPath: config + ".waycloak-original",
		AgentSocket: "/run/waycloak/agent.sock", AgentKeyFile: "/run/waycloak/agent.key", StateDirectory: "/var/lib/cni/waycloak/attachments",
		ReleaseIdentity: wayv1.ReleaseIdentity{Version: "v1.0.0-beta.1", ManifestDigest: "sha256:" + strings.Repeat("a", 64)},
	}
}

func write(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedInstallBootstrapsAndSurvivesPrimaryRegeneration(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	primary := filepath.Join(directory, "10-primary.conflist")
	original := `{"cniVersion":"1.1.0","name":"primary","plugins":[{"type":"bridge","mtu":1450}]}`
	write(t, source, "binary-v1", 0o755)
	write(t, primary, original, 0o644)
	options := fixture(directory, source, filepath.Join(directory, "05-waycloak.conflist"))
	options.SourceConfigPath = primary
	validate := func() error {
		return nodeagent.ValidateCNIInstallation(options.ReceiptPath, options.BinaryPath, options.ConfigPath, options.ReleaseIdentity)
	}
	if err := Install(options); err != nil {
		t.Fatal(err)
	}
	if current, _ := os.ReadFile(primary); string(current) != original {
		t.Fatal("installer modified the upstream-owned primary")
	}
	// Model an infrastructure restart's atomic rewrite, including harmless
	// object-key ordering and whitespace changes.
	rewritten := "{\n\"plugins\":[{\"mtu\":1450,\"type\":\"bridge\"}],\"name\":\"primary\",\"cniVersion\":\"1.1.0\"}\n"
	if err := atomicWrite(primary, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validate(); err != nil {
		t.Fatalf("equivalent infrastructure rewrite withdrew readiness: %v", err)
	}
	if err := Install(options); err != nil {
		t.Fatalf("reinstall after infrastructure rewrite: %v", err)
	}
	if current, _ := os.ReadFile(primary); string(current) != rewritten {
		t.Fatal("reinstall rewrote the upstream primary")
	}
	// Same-release repair and exact new-release replacement are visible to
	// repeated validation; no process-cached receipt or inode is authoritative.
	if err := atomicWrite(options.BinaryPath, []byte("drift"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validate(); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary drift was not detected: %v", err)
	}
	write(t, source, "binary-v2", 0o755)
	options.ReleaseIdentity.Version = "v1.0.0-beta.2"
	if err := Install(options); err != nil {
		t.Fatal(err)
	}
	if err := validate(); err != nil {
		t.Fatalf("replacement installation did not recover: %v", err)
	}
	write(t, primary, strings.Replace(original, "1450", "1400", 1), 0o644)
	if err := validate(); err == nil || !strings.Contains(err.Error(), "topology") {
		t.Fatalf("upstream topology change was not detected: %v", err)
	}
	before, _ := os.ReadFile(options.ConfigPath)
	if err := Install(options); err == nil {
		t.Fatal("reinstall silently adopted a changed upstream topology")
	}
	if after, _ := os.ReadFile(options.ConfigPath); string(after) != string(before) {
		t.Fatal("rejected topology change modified the owned chain")
	}
}

func TestOwnedInstallRefusesAmbiguousOwnershipBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"earlier-config", "foreign-destination", "foreign-backup", "chained-primary", "symlink-primary", "symlink-destination", "symlink-backup", "late-destination"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			source := filepath.Join(directory, "source")
			primary := filepath.Join(directory, "10-primary.conflist")
			original := `{"cniVersion":"1.1.0","name":"primary","plugins":[{"type":"bridge"}]}`
			write(t, source, "binary", 0o755)
			write(t, primary, original, 0o644)
			options := fixture(directory, source, filepath.Join(directory, "05-waycloak.conflist"))
			options.SourceConfigPath = primary
			switch scenario {
			case "earlier-config":
				write(t, filepath.Join(directory, "00-foreign.conf"), "{}", 0o644)
			case "foreign-destination":
				write(t, options.ConfigPath, original, 0o644)
			case "foreign-backup":
				write(t, options.BackupPath, "foreign", 0o600)
			case "chained-primary":
				rendered, _, err := render([]byte(original), options)
				if err != nil {
					t.Fatal(err)
				}
				write(t, primary, string(rendered), 0o644)
			case "late-destination":
				options.ConfigPath = filepath.Join(directory, "20-waycloak.conflist")
			default:
				if os.PathSeparator == '\\' {
					t.Skip("symlink privileges are platform dependent on Windows")
				}
				target := options.ConfigPath
				if scenario == "symlink-primary" {
					target = primary
					if err := os.Remove(primary); err != nil {
						t.Fatal(err)
					}
				} else if scenario == "symlink-backup" {
					target = options.BackupPath
				}
				if err := os.Symlink(source, target); err != nil {
					t.Fatal(err)
				}
			}
			if err := Install(options); err == nil {
				t.Fatal("ambiguous installation was accepted")
			}
			if _, err := os.Lstat(options.BinaryPath); !os.IsNotExist(err) {
				t.Fatalf("rejected installation wrote a binary: %v", err)
			}
			if _, err := os.Lstat(options.ReceiptPath); !os.IsNotExist(err) {
				t.Fatalf("rejected installation wrote a receipt: %v", err)
			}
		})
	}
}
