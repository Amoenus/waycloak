// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cniinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/nodeagent"
)

const PluginType = "waycloak-cni"

type Options struct {
	SourceBinary    string
	BinaryPath      string
	ConfigPath      string
	ReceiptPath     string
	BackupPath      string
	AgentSocket     string
	AgentKeyFile    string
	StateDirectory  string
	ReleaseIdentity wayv1.ReleaseIdentity
}

type conflist struct {
	CNIVersion string            `json:"cniVersion"`
	Name       string            `json:"name"`
	Plugins    []json.RawMessage `json:"plugins"`
}

func Install(options Options) error {
	if err := options.validate(); err != nil {
		return err
	}
	original, err := readRegular(options.ConfigPath, 1<<20)
	if err != nil {
		return fmt.Errorf("read primary CNI conflist: %w", err)
	}
	rendered, alreadyInstalled, err := render(original, options)
	if err != nil {
		return err
	}
	if !alreadyInstalled {
		if err := createExclusive(options.BackupPath, original, 0o600); err != nil {
			return fmt.Errorf("preserve primary CNI conflist: %w", err)
		}
	} else {
		backup, backupErr := readRegular(options.BackupPath, 1<<20)
		if backupErr != nil {
			return fmt.Errorf("verify preserved primary CNI conflist: %w", backupErr)
		}
		expected, backupInstalled, renderErr := render(backup, options)
		if renderErr != nil || backupInstalled || !bytes.Equal(expected, rendered) {
			return errors.New("preserved primary CNI conflist does not reproduce the active chain")
		}
	}
	binary, err := readRegular(options.SourceBinary, 256<<20)
	if err != nil {
		return fmt.Errorf("read CNI binary: %w", err)
	}
	if err := atomicWrite(options.BinaryPath, binary, 0o755); err != nil {
		return fmt.Errorf("install CNI binary: %w", err)
	}
	if err := atomicWrite(options.ConfigPath, rendered, 0o644); err != nil {
		return fmt.Errorf("install chained CNI config: %w", err)
	}
	binarySum, configSum := sha256.Sum256(binary), sha256.Sum256(rendered)
	receipt := nodeagent.CNIInstallationReceipt{
		APIVersion:      nodeagent.InstallationReceiptAPIVersion,
		ReleaseIdentity: options.ReleaseIdentity,
		BinarySHA256:    "sha256:" + hex.EncodeToString(binarySum[:]),
		ConfigSHA256:    "sha256:" + hex.EncodeToString(configSum[:]),
	}
	receiptBytes, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	receiptBytes = append(receiptBytes, '\n')
	if err := atomicWrite(options.ReceiptPath, receiptBytes, 0o600); err != nil {
		return fmt.Errorf("write CNI installation receipt: %w", err)
	}
	return nodeagent.ValidateCNIInstallation(options.ReceiptPath, options.BinaryPath, options.ConfigPath, options.ReleaseIdentity)
}

func (options Options) validate() error {
	for name, value := range map[string]string{
		"source binary": options.SourceBinary, "binary path": options.BinaryPath,
		"config path": options.ConfigPath, "receipt path": options.ReceiptPath,
		"backup path": options.BackupPath,
	} {
		if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	for name, value := range map[string]string{"agent socket": options.AgentSocket, "agent key": options.AgentKeyFile, "state directory": options.StateDirectory} {
		if strings.TrimSpace(value) == "" || !path.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute Linux path", name)
		}
	}
	if options.BinaryPath == options.SourceBinary || options.ConfigPath == options.BackupPath ||
		options.ReleaseIdentity.Version == "" || !validDigest(options.ReleaseIdentity.ManifestDigest) {
		return errors.New("distinct install paths and an exact release identity are required")
	}
	return nil
}

func render(original []byte, options Options) ([]byte, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(original))
	decoder.DisallowUnknownFields()
	var config conflist
	if err := decoder.Decode(&config); err != nil {
		return nil, false, fmt.Errorf("decode primary CNI conflist: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return nil, false, err
	}
	if config.CNIVersion == "" || config.Name == "" || len(config.Plugins) == 0 {
		return nil, false, errors.New("primary CNI conflist must have a version, name, and plugin")
	}
	found := -1
	for index, raw := range config.Plugins {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &header); err != nil || header.Type == "" {
			return nil, false, fmt.Errorf("CNI plugin %d is invalid", index)
		}
		if header.Type == PluginType {
			if found >= 0 {
				return nil, false, errors.New("waycloak occurs more than once in the CNI chain")
			}
			found = index
		}
	}
	plugin, err := json.Marshal(map[string]any{
		"type": PluginType, "agentSocket": options.AgentSocket,
		"agentKeyFile": options.AgentKeyFile, "stateDir": options.StateDirectory,
	})
	if err != nil {
		return nil, false, err
	}
	alreadyInstalled := found >= 0
	if alreadyInstalled {
		if found == 0 || !jsonEqual(config.Plugins[found], plugin) {
			return nil, false, errors.New("existing Waycloak CNI entry is foreign or not chained after the primary plugin")
		}
	} else {
		config.Plugins = append(config.Plugins, plugin)
	}
	rendered, err := json.MarshalIndent(config, "", "  ")
	return append(rendered, '\n'), alreadyInstalled, err
}

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
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
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}

func createExclusive(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(err, closeErr)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".waycloak-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("CNI conflist contains trailing JSON")
	}
	return nil
}

func jsonEqual(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
