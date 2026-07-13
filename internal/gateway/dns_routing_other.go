// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package gateway

import (
	"context"
	"errors"
	"net/netip"
)

type unsupportedDNSRouting struct{}

func NewDNSRouting() DNSRouting { return unsupportedDNSRouting{} }

func (unsupportedDNSRouting) Reconcile(context.Context, netip.Addr) error {
	return errors.New("gateway DNS routing is supported only on Linux")
}
