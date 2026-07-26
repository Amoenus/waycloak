// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditRejectsUnknownAlphaArtifact(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "known.go", "const version = \"v1alpha1\"")
	writeTestFile(t, root, "unknown.yaml", "apiVersion: networking.waycloak.io/v1alpha9")
	inv := testInventory()
	if err := audit(root, inv, []string{"known.go", "unknown.yaml"}); err == nil || !strings.Contains(err.Error(), "unknown.yaml") {
		t.Fatalf("audit() error = %v, want unknown artifact failure", err)
	}
}

func TestAuditRejectsUnknownAlphaArtifactInsideListedRoot(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "alpha/known.go", "const version = \"v1alpha1\"")
	writeTestFile(t, root, "alpha/unknown.go", "const version = \"v1alpha2\"")
	inv := testInventory()
	inv.KnownAlphaPaths = []string{"alpha/known.go"}
	inv.Entries[0].Artifacts["code"] = []string{"alpha/**"}
	if err := audit(root, inv, []string{"alpha/known.go", "alpha/unknown.go"}); err == nil || !strings.Contains(err.Error(), "alpha/unknown.go") {
		t.Fatalf("audit() error = %v, want unknown artifact in listed root failure", err)
	}
}

func TestAuditDistinguishesExactAlphaKeyFromStableQualifiedNames(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "known.go", `const annotation = "networking.waycloak.io/gateway"`)
	writeTestFile(t, root, "stable.go", `const controller = "networking.waycloak.io/gateway-controller"`)
	inv := testInventory()
	inv.Markers = []string{`networking\.waycloak\.io/(gateway)(?:[^A-Za-z0-9._~-]|$)`}
	if err := audit(root, inv, []string{"known.go", "stable.go"}); err != nil {
		t.Fatalf("audit() error = %v, want stable qualified suffix ignored", err)
	}
}

func TestAuditAcceptsCompleteInventory(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "known.go", "const version = \"v1alpha1\"")
	if err := audit(root, testInventory(), []string{"known.go"}); err != nil {
		t.Fatalf("audit() error = %v", err)
	}
}

func TestAuditRejectsStaleKnownAlphaPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "known.go", "package known")
	if err := audit(root, testInventory(), []string{"known.go"}); err == nil || !strings.Contains(err.Error(), "no longer contains an alpha marker") {
		t.Fatalf("audit() error = %v, want stale known path failure", err)
	}
}

func TestAuditRejectsUnapprovedClassification(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "known.go", "const version = \"v1alpha1\"")
	inv := testInventory()
	inv.Entries[0].Classification = "compatibility"
	if err := audit(root, inv, []string{"known.go"}); err == nil || !strings.Contains(err.Error(), "unsupported classification") {
		t.Fatalf("audit() error = %v, want classification failure", err)
	}
}

func testInventory() inventory {
	return inventory{
		SchemaVersion:   1,
		BlockedIssue:    127,
		Markers:         []string{`v1alpha[0-9]+`},
		KnownAlphaPaths: []string{"known.go"},
		Entries: []entry{{
			ID:             "known",
			Classification: "implementation_delete",
			RemovalIssue:   135,
			Contracts:      []string{"test marker"},
			Artifacts: map[string][]string{
				"code": {"known.go"}, "chart": {}, "generated": {}, "tests": {}, "docs": {},
			},
		}},
	}
}

func writeTestFile(t *testing.T, root, path, contents string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
