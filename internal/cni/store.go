// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type FileStore struct{ Directory string }

func (s FileStore) Load(key Key) (Attachment, error) {
	if err := key.Validate(); err != nil {
		return Attachment{}, err
	}
	data, err := os.ReadFile(s.path(key))
	if err != nil {
		return Attachment{}, err
	}
	var attachment Attachment
	if err := json.Unmarshal(data, &attachment); err != nil {
		return Attachment{}, fmt.Errorf("decode attachment state: %w", err)
	}
	if err := attachment.Validate(); err != nil {
		return Attachment{}, fmt.Errorf("validate attachment state: %w", err)
	}
	if attachment.Key() != key {
		return Attachment{}, errors.New("attachment state key does not match its filename")
	}
	return attachment, nil
}

func (s FileStore) Save(attachment Attachment) error {
	if err := attachment.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Directory, 0o700); err != nil {
		return fmt.Errorf("create CNI state directory: %w", err)
	}
	data, err := json.Marshal(attachment)
	if err != nil {
		return fmt.Errorf("encode attachment state: %w", err)
	}
	temporary, err := os.CreateTemp(s.Directory, ".waycloak-attachment-*")
	if err != nil {
		return fmt.Errorf("create temporary attachment state: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, s.path(attachment.Key())); err != nil {
		return fmt.Errorf("publish attachment state: %w", err)
	}
	return nil
}

func (s FileStore) Delete(key Key) error {
	if err := key.Validate(); err != nil {
		return err
	}
	err := os.Remove(s.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s FileStore) List(network string) ([]Attachment, error) {
	if network == "" {
		return nil, errors.New("CNI network name is required")
	}
	entries, err := os.ReadDir(s.Directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	attachments := make([]Attachment, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var attachment Attachment
		if err := json.Unmarshal(data, &attachment); err != nil {
			return nil, fmt.Errorf("decode attachment state %q: %w", entry.Name(), err)
		}
		if err := attachment.Validate(); err != nil {
			return nil, fmt.Errorf("validate attachment state %q: %w", entry.Name(), err)
		}
		if attachment.Network == network {
			attachments = append(attachments, attachment)
		}
	}
	return attachments, nil
}

// ListAll returns every validated Waycloak-owned attachment. The node agent
// uses it to rebuild exact state after restart without guessing CNI network
// names or inspecting unrelated runtime files.
func (s FileStore) ListAll() ([]Attachment, error) {
	entries, err := os.ReadDir(s.Directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	attachments := make([]Attachment, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var attachment Attachment
		if err := json.Unmarshal(data, &attachment); err != nil {
			return nil, fmt.Errorf("decode attachment state %q: %w", entry.Name(), err)
		}
		if err := attachment.Validate(); err != nil {
			return nil, fmt.Errorf("validate attachment state %q: %w", entry.Name(), err)
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func (s FileStore) path(key Key) string {
	sum := sha256.Sum256([]byte(key.Network + "\x00" + key.ContainerID + "\x00" + key.IfName))
	return filepath.Join(s.Directory, hex.EncodeToString(sum[:])+".json")
}
