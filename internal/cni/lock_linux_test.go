// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package cni

import (
	"context"
	"testing"
	"time"
)

func TestAttachmentLockSerializesSamePodUID(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireAttachmentLock(context.Background(), directory, "pod-uid")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	acquired := make(chan *AttachmentLock, 1)
	errs := make(chan error, 1)
	go func() {
		second, err := AcquireAttachmentLock(context.Background(), directory, "pod-uid")
		if err != nil {
			errs <- err
			return
		}
		acquired <- second
	}()
	select {
	case lock := <-acquired:
		_ = lock.Close()
		t.Fatal("second same-Pod lock was acquired before the first was released")
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case lock := <-acquired:
		defer lock.Close()
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("second same-Pod lock did not proceed after release")
	}
}

func TestAttachmentLockDoesNotSerializeDifferentPodUIDs(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireAttachmentLock(context.Background(), directory, "pod-one")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := AcquireAttachmentLock(context.Background(), directory, "pod-two")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
}
