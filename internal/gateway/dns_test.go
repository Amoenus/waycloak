// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestResolverConfigFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	contents := "nameserver 10.43.0.10\nsearch apps.svc.cluster.local svc.cluster.local cluster.local\noptions ndots:5\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := ResolverConfigFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ClusterUpstream != netip.MustParseAddrPort("10.43.0.10:53") {
		t.Fatalf("cluster upstream = %s", config.ClusterUpstream)
	}
	wantZones := []string{"apps.svc.cluster.local.", "svc.cluster.local.", "cluster.local."}
	if !reflect.DeepEqual(config.ClusterZones, wantZones) {
		t.Fatalf("cluster zones = %#v", config.ClusterZones)
	}
}

func TestDNSProxyRoutesOnlyClusterSuffixesToClusterResolver(t *testing.T) {
	cluster := netip.MustParseAddrPort("10.43.0.10:53")
	external := netip.MustParseAddrPort("127.0.0.1:53")
	proxy := &DNSProxy{ClusterUpstream: cluster, ExternalUpstream: external, ClusterZones: []string{"cluster.local."}, Port: DNSPort}
	for name, want := range map[string]netip.AddrPort{
		"kubernetes.default.svc.cluster.local.": cluster,
		"cluster.local.":                        cluster,
		"example.com.":                          external,
		"notcluster.local.example.":             external,
	} {
		upstream, err := proxy.upstreamFor(dnsQuery(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if upstream != want {
			t.Fatalf("query %s routed to %s, want %s", name, upstream, want)
		}
	}
}

func dnsQuery(t *testing.T, name string) []byte {
	t.Helper()
	parsed, err := dnsmessage.NewName(name)
	if err != nil {
		t.Fatal(err)
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 1, RecursionDesired: true})
	if err := builder.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := builder.Question(dnsmessage.Question{Name: parsed, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}); err != nil {
		t.Fatal(err)
	}
	message, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return message
}
