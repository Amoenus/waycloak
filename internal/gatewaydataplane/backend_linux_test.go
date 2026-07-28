// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package gatewaydataplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequireIPv4Forwarding(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "enabled", value: "1\n"},
		{name: "disabled", value: "0\n", wantErr: "forwarding is disabled"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "ip_forward")
			if err := os.WriteFile(path, []byte(test.value), 0o600); err != nil {
				t.Fatal(err)
			}
			err := requireIPv4Forwarding(path)
			if test.wantErr == "" && err != nil {
				t.Fatalf("require forwarding: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("require forwarding error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestRequireIPv4ForwardingMissing(t *testing.T) {
	t.Parallel()
	err := requireIPv4Forwarding(filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "observe namespaced IPv4 forwarding") {
		t.Fatalf("require forwarding error = %v", err)
	}
}
