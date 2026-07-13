// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package dataplane

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

type recordingBackend struct {
	calls        []string
	configureErr error
}

func (b *recordingBackend) Preflight(context.Context) error { return nil }
func (b *recordingBackend) InstallLockdown(context.Context, string) error {
	b.calls = append(b.calls, "lockdown")
	return nil
}
func (b *recordingBackend) Configure(context.Context, Config) error {
	b.calls = append(b.calls, "configure")
	return b.configureErr
}
func (b *recordingBackend) Verify(context.Context, Config) error { return nil }
func (b *recordingBackend) Repair(context.Context, Config) error { return nil }

func validConfig() Config {
	return Config{
		PodUID:             "00000000-0000-0000-0000-000000000001",
		Address:            netip.MustParsePrefix("172.30.99.2/24"),
		OverlayCIDR:        netip.MustParsePrefix("172.30.99.0/24"),
		GatewayAddress:     netip.MustParseAddr("172.30.99.1"),
		GatewayEndpoint:    netip.MustParseAddrPort("10.0.0.2:4789"),
		GatewayHealthPort:  18080,
		VNI:                7999,
		MTU:                1320,
		ClusterTrafficMode: ClusterTrafficPreserve,
	}
}

func TestPrepareInstallsLockdownBeforeValidation(t *testing.T) {
	b := &recordingBackend{}
	cfg := validConfig()
	cfg.GatewayEndpoint = netip.AddrPort{}
	err := (Agent{Backend: b}).Prepare(context.Background(), cfg)
	if err == nil {
		t.Fatal("Prepare() succeeded with an invalid endpoint")
	}
	if !reflect.DeepEqual(b.calls, []string{"lockdown"}) {
		t.Fatalf("calls = %v, want lockdown before validation", b.calls)
	}
}

func TestPrepareRetainsLockdownWhenConfigurationFails(t *testing.T) {
	b := &recordingBackend{configureErr: errors.New("vxlan failed")}
	err := (Agent{Backend: b}).Prepare(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Prepare() succeeded when configuration failed")
	}
	if !reflect.DeepEqual(b.calls, []string{"lockdown", "configure"}) {
		t.Fatalf("calls = %v", b.calls)
	}
}
