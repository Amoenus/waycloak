// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import "testing"

func TestIsPrerelease(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		prerelease bool
		valid      bool
	}{
		{name: "stable", version: "0.2.0", valid: true},
		{name: "alpha", version: "0.2.0-alpha.1", prerelease: true, valid: true},
		{name: "beta", version: "1.0.0-beta.2", prerelease: true, valid: true},
		{name: "release candidate", version: "1.0.0-rc.1", prerelease: true, valid: true},
		{name: "leading v", version: "v0.2.0-alpha.1"},
		{name: "missing patch", version: "0.2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := isPrerelease(test.version)
			if (err == nil) != test.valid {
				t.Fatalf("isPrerelease(%q) error = %v", test.version, err)
			}
			if err == nil && actual != test.prerelease {
				t.Fatalf("isPrerelease(%q) = %t, want %t", test.version, actual, test.prerelease)
			}
		})
	}
}
