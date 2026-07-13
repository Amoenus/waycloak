// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package proton

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func dialTunnel(ctx context.Context, network, address, tunnelInterface string) (net.Conn, error) {
	dialer := net.Dialer{}
	if tunnelInterface != "" {
		dialer.Control = func(_, _ string, raw syscall.RawConn) error {
			var bindErr error
			if err := raw.Control(func(fd uintptr) {
				bindErr = unix.BindToDevice(int(fd), tunnelInterface)
			}); err != nil {
				return err
			}
			if bindErr != nil {
				return fmt.Errorf("bind NAT-PMP socket to %s: %w", tunnelInterface, bindErr)
			}
			return nil
		}
	}
	return dialer.DialContext(ctx, network, address)
}
