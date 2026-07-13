// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	values := map[string]string{
		"version": "v1alpha1", "podUID": "uid-1", "address": "172.30.99.2",
		"overlayCIDR": "172.30.99.0/24", "gatewayAddress": "172.30.99.1",
		"gatewayEndpoint": "10.0.0.2:4789", "gatewayHealthPort": "18080", "vni": "7999", "mtu": "1320",
		"clusterTrafficMode": "Preserve", "clusterCIDRs": "10.42.0.0/16, 10.43.0.0/16\n",
	}
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Address.String(); got != "172.30.99.2/24" {
		t.Fatalf("address = %s", got)
	}
	if len(cfg.ClusterCIDRs) != 2 {
		t.Fatalf("cluster CIDRs = %v", cfg.ClusterCIDRs)
	}
}

func TestLoadRejectsMissingObservedEndpoint(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{"version": "v1alpha1", "podUID": "uid-1", "address": "172.30.99.2", "overlayCIDR": "172.30.99.0/24", "gatewayAddress": "172.30.99.1", "gatewayEndpoint": "", "gatewayHealthPort": "18080", "vni": "7999", "mtu": "1320", "clusterTrafficMode": "Preserve"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load() accepted a missing observed endpoint")
	}
}
