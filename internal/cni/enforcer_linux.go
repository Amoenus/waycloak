// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package cni

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/Amoenus/waycloak/internal/dataplane"
	"github.com/containernetworking/plugins/pkg/ns"
)

type LinuxEnforcer struct{ Backend dataplane.Backend }

func (e LinuxEnforcer) Identity(path string) (string, error) {
	if path == "" {
		return "", os.ErrNotExist
	}
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", uint64(stat.Dev), stat.Ino), nil
}

func (e LinuxEnforcer) InstallLockdown(ctx context.Context, path, podUID string) error {
	return e.inNamespace(path, func(agent dataplane.Agent) error {
		return agent.Backend.InstallLockdown(ctx, podUID)
	})
}

func (e LinuxEnforcer) Configure(ctx context.Context, path string, cfg dataplane.Config) error {
	return e.inNamespace(path, func(agent dataplane.Agent) error {
		return agent.Backend.Configure(ctx, cfg)
	})
}

func (e LinuxEnforcer) Verify(ctx context.Context, path string, cfg dataplane.Config) error {
	return e.inNamespace(path, func(agent dataplane.Agent) error {
		return agent.Backend.Verify(ctx, cfg)
	})
}

func (e LinuxEnforcer) Cleanup(ctx context.Context, path, podUID string) error {
	return e.inNamespace(path, func(agent dataplane.Agent) error {
		return agent.Cleanup(ctx, podUID, nil)
	})
}

func (e LinuxEnforcer) inNamespace(path string, operation func(dataplane.Agent) error) error {
	if e.Backend == nil {
		return fmt.Errorf("data-plane backend is required")
	}
	return ns.WithNetNSPath(path, func(ns.NetNS) error {
		return operation(dataplane.Agent{Backend: e.Backend})
	})
}
