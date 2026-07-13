// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"net/netip"
	"strings"
	"testing"
)

func TestRenderEnginePostRulesAllowsOnlyObservedClusterResolver(t *testing.T) {
	rendered, err := RenderEnginePostRules("iptables --policy FORWARD ACCEPT\n", ResolverConfig{ClusterUpstream: netip.MustParseAddrPort("10.43.0.10:53")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"iptables --policy FORWARD ACCEPT",
		"iptables --append OUTPUT --destination 10.43.0.10/32 --protocol udp --destination-port 53 --jump ACCEPT",
		"iptables --append OUTPUT --destination 10.43.0.10/32 --protocol tcp --destination-port 53 --jump ACCEPT",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered rules do not contain %q: %s", want, rendered)
		}
	}
	if strings.Contains(rendered, "0.0.0.0/0") {
		t.Fatalf("rendered rules contain a broad DNS exception: %s", rendered)
	}
}

func TestRenderEnginePostRulesRejectsNonDNSUpstream(t *testing.T) {
	if _, err := RenderEnginePostRules("", ResolverConfig{ClusterUpstream: netip.MustParseAddrPort("10.43.0.10:5353")}); err == nil {
		t.Fatal("non-standard cluster resolver port was accepted")
	}
}
