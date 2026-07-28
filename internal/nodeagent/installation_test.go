// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

func TestValidateCNIInstallationRequiresExactProtectedArtifacts(t *testing.T) {
	directory := t.TempDir()
	binaryPath := filepath.Join(directory, "waycloak-cni")
	configPath := filepath.Join(directory, "10-kindnet.conflist")
	receiptPath := filepath.Join(directory, "install-receipt.json")
	release := wayv1.ReleaseIdentity{Version: "v1.0.0-beta.1", ManifestDigest: "sha256:4444444444444444444444444444444444444444444444444444444444444444"}
	binary := []byte("exact-cni-binary")
	config := []byte(`{"cniVersion":"1.1.0","plugins":[{"type":"kindnet"},{"type":"waycloak-cni"}]}`)
	writeProtected(t, binaryPath, binary)
	writeProtected(t, configPath, config)
	receipt := CNIInstallationReceipt{APIVersion: InstallationReceiptAPIVersion, ReleaseIdentity: release, BinarySHA256: digest(binary), ConfigSHA256: digest(config)}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	writeProtected(t, receiptPath, receiptBytes)
	if err := ValidateCNIInstallation(receiptPath, binaryPath, configPath, release); err != nil {
		t.Fatal(err)
	}

	writeProtected(t, binaryPath, []byte("tampered"))
	if err := ValidateCNIInstallation(receiptPath, binaryPath, configPath, release); err == nil {
		t.Fatal("tampered CNI binary was accepted")
	}
	writeProtected(t, binaryPath, binary)
	writeProtected(t, configPath, []byte(`{"cniVersion":"1.1.0","plugins":[{"type":"kindnet"}]}`))
	if err := ValidateCNIInstallation(receiptPath, binaryPath, configPath, release); err == nil {
		t.Fatal("CNI config without Waycloak was accepted")
	}
	writeProtected(t, configPath, config)
	skewed := release
	skewed.Version = "v2.0.0"
	if err := ValidateCNIInstallation(receiptPath, binaryPath, configPath, skewed); err == nil {
		t.Fatal("release-skewed CNI receipt was accepted")
	}
}

func TestValidateCNIInstallationRejectsWritableOrSymlinkedReceipt(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows does not enforce Unix group/world mode bits")
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "receipt")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedRegular(symlink, 1024); err == nil {
		t.Fatal("symlinked receipt was accepted")
	}
	if err := os.Chmod(target, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedRegular(target, 1024); err == nil {
		t.Fatal("group-writable receipt was accepted")
	}
}

func writeProtected(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
