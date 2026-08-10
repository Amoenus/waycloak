// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package cni

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const attachmentLockTimeout = 30 * time.Second

// AttachmentLock serializes CNI operations that can replace or remove the
// same Pod-UID data-plane policy across independent runtime processes.
type AttachmentLock struct{ file *os.File }

func AcquireAttachmentLock(parent context.Context, stateDirectory, podUID string) (*AttachmentLock, error) {
	if stateDirectory == "" || podUID == "" {
		return nil, errors.New("CNI state directory and Pod UID are required for attachment locking")
	}
	directory := filepath.Join(stateDirectory, ".locks")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create CNI attachment lock directory: %w", err)
	}
	digest := sha256.Sum256([]byte(podUID))
	file, err := os.OpenFile(filepath.Join(directory, hex.EncodeToString(digest[:])+".lock"), os.O_CREATE|os.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open CNI attachment lock: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, attachmentLockTimeout)
	defer cancel()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &AttachmentLock{file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire CNI attachment lock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("acquire CNI attachment lock within %s: %w", attachmentLockTimeout, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (l *AttachmentLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	return errors.Join(err, l.file.Close())
}
