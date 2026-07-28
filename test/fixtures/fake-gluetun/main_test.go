// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import "testing"

func TestParseInterfacePrefixPreservesHostAddress(t *testing.T) {
	prefix, err := parseInterfacePrefix("10.200.0.2/30")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := prefix.Addr().String(), "10.200.0.2"; got != want {
		t.Fatalf("interface address = %s, want %s", got, want)
	}
}

func TestParseInterfacePrefixRejectsInvalidAddress(t *testing.T) {
	if _, err := parseInterfacePrefix("not-an-address"); err == nil {
		t.Fatal("invalid interface address unexpectedly accepted")
	}
}
