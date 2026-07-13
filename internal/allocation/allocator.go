// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package allocation

import (
	"errors"
	"fmt"
	"net/netip"
)

var ErrExhausted = errors.New("overlay address pool exhausted")

// Next returns the lowest free client address. The network address, first usable
// gateway address, and IPv4 broadcast address are never allocated.
func Next(cidr string, used map[netip.Addr]struct{}) (netip.Addr, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() {
		return netip.Addr{}, fmt.Errorf("invalid IPv4 overlay CIDR %q", cidr)
	}
	prefix = prefix.Masked()
	if prefix.Bits() > 30 {
		return netip.Addr{}, fmt.Errorf("overlay CIDR %q has no client addresses", cidr)
	}
	addr := prefix.Addr().Next().Next()
	for prefix.Contains(addr) {
		if addr.Next().IsValid() && prefix.Contains(addr.Next()) {
			if _, exists := used[addr]; !exists {
				return addr, nil
			}
		}
		addr = addr.Next()
	}
	return netip.Addr{}, ErrExhausted
}

func GatewayAddress(cidr string) (netip.Addr, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() {
		return netip.Addr{}, fmt.Errorf("invalid IPv4 overlay CIDR %q", cidr)
	}
	return p.Masked().Addr().Next(), nil
}
