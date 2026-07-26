// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LoadProtocolKey reads a key only through the required root-only boundary.
func LoadProtocolKey(path string) ([]byte, error) {
	directoryInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		return nil, errors.New("local protocol directory must be a non-symlink directory")
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("local protocol directory permissions must be 0700, got %04o", directoryInfo.Mode().Perm())
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("local protocol key must be a regular non-symlink file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("local protocol key permissions must be 0600, got %04o", info.Mode().Perm())
	}
	if err := validateProtocolKeyOwner(directoryInfo); err != nil {
		return nil, err
	}
	if err := validateProtocolKeyOwner(info); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != ProtocolKeySize {
		return nil, fmt.Errorf("local protocol key must be exactly %d bytes", ProtocolKeySize)
	}
	return key, nil
}

// RotateProtocolKey atomically replaces the per-agent-start authentication key.
func RotateProtocolKey(path string) ([]byte, error) {
	directory := filepath.Dir(path)
	if err := rejectProtocolKeySymlinks(directory, path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create local protocol directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("restrict local protocol directory: %w", err)
	}
	key := make([]byte, ProtocolKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate local protocol key: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".waycloak-auth-*")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if _, err := temporary.Write(key); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return nil, err
	}
	if loaded, err := LoadProtocolKey(path); err != nil || !bytes.Equal(loaded, key) {
		return nil, errors.Join(errors.New("validate rotated local protocol key"), err)
	}
	return key, nil
}

func rejectProtocolKeySymlinks(directory, path string) error {
	directoryInfo, err := os.Lstat(directory)
	if err == nil {
		if directoryInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("local protocol directory must not be a symlink during rotation")
		}
		if !directoryInfo.IsDir() {
			return errors.New("local protocol directory path is not a directory")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect local protocol directory before rotation: %w", err)
	}

	keyInfo, err := os.Lstat(path)
	if err == nil && keyInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("local protocol key must not be a symlink during rotation")
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect local protocol key before rotation: %w", err)
	}
	return nil
}
