// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteControlAuthenticationProducesExactRestrictedPolicy(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "runtime.key")
	config := filepath.Join(directory, "control", "auth.toml")
	apiKey := filepath.Join(directory, "control", "api-key")
	if err := os.WriteFile(source, bytes.Repeat([]byte{0x5a}, 128), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := WriteControlAuthentication(source, config, apiKey); err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(apiKey)
	if err != nil {
		t.Fatal(err)
	}
	keyValue := strings.TrimSpace(string(key))
	if len(keyValue) != 64 || !strings.Contains(string(policy), `auth = "apikey"`) || !strings.Contains(string(policy), `apikey = "`+keyValue+`"`) {
		t.Fatalf("generated control identity is malformed")
	}
	for _, route := range authenticatedControlRoutes {
		if !strings.Contains(string(policy), `"`+route+`"`) {
			t.Fatalf("policy lacks %q", route)
		}
	}
	for _, forbidden := range []string{"PUT ", "/settings", "/v1/vpn/status"} {
		if strings.Contains(string(policy), forbidden) {
			t.Fatalf("policy includes forbidden route fragment %q", forbidden)
		}
	}
	if strings.Contains(string(policy), string(bytes.Repeat([]byte{0x5a}, 16))) {
		t.Fatal("source key material was copied into control policy")
	}
}
