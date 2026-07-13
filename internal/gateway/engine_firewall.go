// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

func RenderEnginePostRules(base string, resolver ResolverConfig) (string, error) {
	upstream := resolver.ClusterUpstream
	if !upstream.IsValid() || upstream.Port() != DNSPort {
		return "", errors.New("cluster resolver must be a valid port-53 address")
	}
	address := upstream.Addr().Unmap()
	command := "iptables"
	if address.Is6() {
		command = "ip6tables"
	}
	prefix := netip.PrefixFrom(address, address.BitLen())
	var rendered strings.Builder
	rendered.WriteString(strings.TrimRight(base, "\n"))
	rendered.WriteByte('\n')
	for _, protocol := range []string{"udp", "tcp"} {
		fmt.Fprintf(&rendered, "%s --append OUTPUT --destination %s --protocol %s --destination-port %d --jump ACCEPT\n", command, prefix, protocol, DNSPort)
	}
	return rendered.String(), nil
}
