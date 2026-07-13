// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import "testing"

func TestEngineForRejectsUnsupportedTypes(t *testing.T) {
	if _, err := engineFor("FakeVPN", "", ""); err == nil {
		t.Fatal("unsupported engine was accepted")
	}
	if _, err := engineFor("Gluetun", "http://127.0.0.1:9999/", "http://127.0.0.1:8000"); err != nil {
		t.Fatal(err)
	}
}
