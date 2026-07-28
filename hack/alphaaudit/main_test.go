// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditRejectsRemovedPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "api/v1alpha1/types.go", "package v1alpha1")
	err := audit(root, testPolicy(), []string{"api/v1alpha1/types.go"})
	if err == nil || !strings.Contains(err.Error(), "removed alpha path exists") {
		t.Fatalf("audit error = %v", err)
	}
}

func TestAuditRejectsMarkerOnShippedSurface(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "cmd/controller/main.go", "const old = \"networking.waycloak.io/gateway\"")
	value := testPolicy()
	value.ForbiddenPaths = []string{"api/v1alpha1/**"}
	err := audit(root, value, []string{"cmd/controller/main.go"})
	if err == nil || !strings.Contains(err.Error(), "forbidden marker") {
		t.Fatalf("audit error = %v", err)
	}
}

func TestAuditRejectsUnknownAlphaTest(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "internal/new/runtime_test.go", "const old = \"networking.waycloak.io/gateway\"")
	err := audit(root, testPolicy(), []string{"internal/new/runtime_test.go"})
	if err == nil || !strings.Contains(err.Error(), "forbidden marker") {
		t.Fatalf("audit error = %v", err)
	}
}

func TestAuditAllowsHistoricalDocsAndNegativeTests(t *testing.T) {
	root := t.TempDir()
	paths := []string{"docs/api/api-contract.md", "internal/enrollment/resolver_test.go"}
	for _, path := range paths {
		writeTestFile(t, root, path, "networking.waycloak.io/gateway")
	}
	if err := audit(root, testPolicy(), paths); err != nil {
		t.Fatal(err)
	}
}

func TestAuditRejectsInvalidPolicy(t *testing.T) {
	if err := audit(t.TempDir(), policy{}, nil); err == nil {
		t.Fatal("empty policy passed")
	}
}

func testPolicy() policy {
	return policy{
		SchemaVersion:    1,
		AuditedPaths:     []string{"api/**", "cmd/**", "internal/**"},
		ExemptPaths:      []string{"internal/enrollment/resolver_test.go"},
		ForbiddenPaths:   []string{"api/v1alpha1/**", "cmd/agent/**"},
		ForbiddenMarkers: []string{`networking\.waycloak\.io/gateway(?:["'[:space:]]|$)`},
	}
}

func writeTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
