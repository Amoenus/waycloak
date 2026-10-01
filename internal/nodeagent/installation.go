// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

const InstallationReceiptAPIVersion = "cni-installation.waycloak.io/v1"

type CNIInstallationReceipt struct {
	APIVersion          string                `json:"apiVersion"`
	ReleaseIdentity     wayv1.ReleaseIdentity `json:"releaseIdentity"`
	BinarySHA256        string                `json:"binarySHA256"`
	ConfigSHA256        string                `json:"configSHA256"`
	PrimaryConfigName   string                `json:"primaryConfigName,omitempty"`
	PrimaryConfigSHA256 string                `json:"primaryConfigSHA256,omitempty"`
}

func ValidateCNIInstallation(receiptPath, binaryPath, configPath string, expected wayv1.ReleaseIdentity) error {
	return validateCNIInstallation(receiptPath, binaryPath, configPath, expected, false)
}

// ValidateCNIInstallationForDenyHold validates exact source artifacts during a
// journal-bound release transition. The successor may still have the old chart's
// individual file mounts, so directory selection cannot yet be observed. This
// must only be used by a transition-held service: it never authorizes readiness,
// ADD, CHECK, or reopening an attachment. Normal operation uses the full check.
func ValidateCNIInstallationForDenyHold(receiptPath, binaryPath, configPath string, expected wayv1.ReleaseIdentity) error {
	return validateCNIInstallation(receiptPath, binaryPath, configPath, expected, true)
}

func validateCNIInstallation(receiptPath, binaryPath, configPath string, expected wayv1.ReleaseIdentity, denyHold bool) error {
	if receiptPath == "" || binaryPath == "" || configPath == "" || expected.Version == "" || expected.ManifestDigest == "" {
		return errors.New("CNI receipt, binary, config, and exact release identity are required")
	}
	receiptBytes, err := readProtectedRegular(receiptPath, 64<<10)
	if err != nil {
		return fmt.Errorf("read CNI installation receipt: %w", err)
	}
	var receipt CNIInstallationReceipt
	decoder := json.NewDecoder(bytes.NewReader(receiptBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return fmt.Errorf("decode CNI installation receipt: %w", err)
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("CNI installation receipt contains trailing JSON")
	}
	if receipt.APIVersion != InstallationReceiptAPIVersion || receipt.ReleaseIdentity != expected || !validDigest(receipt.BinarySHA256) || !validDigest(receipt.ConfigSHA256) {
		return errors.New("CNI installation receipt identity is unsupported")
	}
	binaryDigest, err := digestProtectedRegular(binaryPath, 256<<20)
	if err != nil {
		return fmt.Errorf("verify installed CNI binary: %w", err)
	}
	configBytes, err := readProtectedRegular(configPath, 1<<20)
	if err != nil {
		return fmt.Errorf("verify installed CNI config: %w", err)
	}
	configSum := sha256.Sum256(configBytes)
	if binaryDigest != receipt.BinarySHA256 {
		return errors.New("installed CNI binary does not match the signed-plan receipt; rerun the exact release installer")
	}
	if "sha256:"+hex.EncodeToString(configSum[:]) != receipt.ConfigSHA256 {
		return errors.New("installed CNI config does not match the signed-plan receipt; inspect config ownership and rerun the exact release installer")
	}
	if err := requireWaycloakChain(configBytes); err != nil {
		return err
	}
	if denyHold {
		return nil
	}
	if err := ValidateCNIConfigSelection(configPath); err != nil {
		return err
	}
	if receipt.PrimaryConfigName != "" || receipt.PrimaryConfigSHA256 != "" {
		name := receipt.PrimaryConfigName
		if filepath.Base(name) != name || !strings.HasSuffix(name, ".conflist") || name <= filepath.Base(configPath) || !validDigest(receipt.PrimaryConfigSHA256) {
			return errors.New("CNI receipt has an invalid primary config identity")
		}
		primary, err := readProtectedRegular(filepath.Join(filepath.Dir(configPath), name), 1<<20)
		if err != nil {
			return fmt.Errorf("verify upstream primary CNI config: %w", err)
		}
		digest, err := PrimaryConfigDigest(primary)
		if err != nil || digest != receipt.PrimaryConfigSHA256 {
			return errors.New("upstream primary CNI config changed; review the topology and update the owned chain before restoring readiness")
		}
	}
	return nil
}

// ValidateCNIConfigSelection enforces the supported single-network runtime's
// first-file selection contract. Treat even invalid earlier files and symlinks
// as conflicts: silently assuming that a runtime will skip them is unsafe.
// The installer also calls this before creating the destination.
func ValidateCNIConfigSelection(configPath string) error {
	name := filepath.Base(configPath)
	if !strings.HasSuffix(name, ".conflist") {
		return errors.New("installed config must be a .conflist in the runtime CNI config directory")
	}
	entries, err := os.ReadDir(filepath.Dir(configPath))
	if err != nil {
		return fmt.Errorf("inspect CNI config selection: %w", err)
	}
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if !entry.IsDir() && (ext == ".conf" || ext == ".conflist" || ext == ".json") && entry.Name() < name {
			return fmt.Errorf("CNI config %q is shadowed by earlier config %q; review runtime config selection", name, entry.Name())
		}
	}
	return nil
}

// PrimaryConfigDigest ignores formatting and object-key order, but retains
// every topology field and plugin order. Infrastructure rewrites of equivalent
// configuration are harmless; changes of network meaning require review.
func PrimaryConfigDigest(data []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("primary CNI config contains trailing JSON")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func readProtectedRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !protectedFileMode(info.Mode()) {
		return nil, errors.New("file must be regular and not group/world writable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds the size limit")
	}
	return data, nil
}

func digestProtectedRegular(path string, limit int64) (string, error) {
	data, err := readProtectedRegular(path, limit)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func requireWaycloakChain(config []byte) error {
	var conflist struct {
		Plugins []struct {
			Type string `json:"type"`
		} `json:"plugins"`
	}
	decoder := json.NewDecoder(bytes.NewReader(config))
	if err := decoder.Decode(&conflist); err != nil {
		return fmt.Errorf("decode installed CNI conflist: %w", err)
	}
	count, index := 0, -1
	for i, plugin := range conflist.Plugins {
		if plugin.Type == "waycloak-cni" {
			count, index = count+1, i
		}
	}
	if count != 1 || index < 1 {
		return errors.New("installed CNI config must contain Waycloak exactly once after a primary plugin")
	}
	return nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
