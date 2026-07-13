// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package proton

import (
	"context"
	"errors"
	"net"
)

func dialTunnel(ctx context.Context, network, address, tunnelInterface string) (net.Conn, error) {
	if tunnelInterface != "" {
		return nil, errors.New("binding the Proton NAT-PMP socket to a tunnel is supported only on Linux")
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
