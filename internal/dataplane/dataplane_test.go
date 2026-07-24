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
	repairErr    error
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
func (b *recordingBackend) Verify(context.Context, Config) error {
	b.calls = append(b.calls, "verify")
	return nil
}
func (b *recordingBackend) Repair(context.Context, Config) error {
	b.calls = append(b.calls, "repair")
	return b.repairErr
}

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

func TestVerifyReinstallsLockdownBeforeRepairingCurrentConfiguration(t *testing.T) {
	b := &recordingBackend{}
	if err := (Agent{Backend: b}).Verify(context.Background(), validConfig()); err != nil {
		t.Fatalf("Verify() failed: %v", err)
	}
	if !reflect.DeepEqual(b.calls, []string{"lockdown", "repair"}) {
		t.Fatalf("calls = %v, want lockdown before repair", b.calls)
	}
}

func TestVerifyRetainsLockdownWhenCurrentConfigurationIsInvalid(t *testing.T) {
	b := &recordingBackend{}
	cfg := validConfig()
	cfg.GatewayEndpoint = netip.AddrPort{}
	err := (Agent{Backend: b}).Verify(context.Background(), cfg)
	if err == nil {
		t.Fatal("Verify() succeeded with an invalid current endpoint")
	}
	if !reflect.DeepEqual(b.calls, []string{"lockdown"}) {
		t.Fatalf("calls = %v, want lockdown before validation", b.calls)
	}
}

func TestVerifyRetainsLockdownWhenCurrentConfigurationRepairFails(t *testing.T) {
	b := &recordingBackend{repairErr: errors.New("replace stale VXLAN endpoint")}
	err := (Agent{Backend: b}).Verify(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Verify() succeeded when current configuration repair failed")
	}
	if !reflect.DeepEqual(b.calls, []string{"lockdown", "repair"}) {
		t.Fatalf("calls = %v, want lockdown before failed repair", b.calls)
	}
}

func TestConfigValidatesApplicationPortRedirects(t *testing.T) {
	cfg := validConfig()
	cfg.ApplicationPortRedirects = []ApplicationPortRedirect{{Identity: "lease-uid", TargetPort: 6881, ApplicationPort: 42000, Protocols: []string{"TCP", "UDP"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid application redirect: %v", err)
	}
	cfg.ApplicationPortRedirects[0].ApplicationPort = 6881
	if err := cfg.Validate(); err == nil {
		t.Fatal("identity redirect was accepted")
	}
	cfg = validConfig()
	cfg.ApplicationPortRedirects = []ApplicationPortRedirect{
		{Identity: "lease-one", TargetPort: 6881, ApplicationPort: 42000, Protocols: []string{"TCP"}},
		{Identity: "lease-two", TargetPort: 6881, ApplicationPort: 42001, Protocols: []string{"TCP"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("duplicate target-port/protocol redirect was accepted")
	}
}
