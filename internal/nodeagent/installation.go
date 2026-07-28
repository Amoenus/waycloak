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
	"strings"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

const InstallationReceiptAPIVersion = "cni-installation.waycloak.io/v1"

type CNIInstallationReceipt struct {
	APIVersion      string                `json:"apiVersion"`
	ReleaseIdentity wayv1.ReleaseIdentity `json:"releaseIdentity"`
	BinarySHA256    string                `json:"binarySHA256"`
	ConfigSHA256    string                `json:"configSHA256"`
}

func ValidateCNIInstallation(receiptPath, binaryPath, configPath string, expected wayv1.ReleaseIdentity) error {
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
	if binaryDigest != receipt.BinarySHA256 || "sha256:"+hex.EncodeToString(configSum[:]) != receipt.ConfigSHA256 {
		return errors.New("installed CNI binary or config does not match the signed-plan receipt")
	}
	if err := requireWaycloakChain(configBytes); err != nil {
		return err
	}
	return nil
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
