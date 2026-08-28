// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDirectGoModules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go.mod")
	data := "module example.test/x\n\nrequire (\n example.test/direct v1.2.3\n example.test/indirect v1.0.0 // indirect\n)\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := parseDirectGoModules(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["example.test/direct"] != "v1.2.3" {
		t.Fatalf("unexpected modules: %#v", got)
	}
}

func TestVerifyRejectsUnownedLagAndOverBudgetBinary(t *testing.T) {
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	base := inventory{
		APIVersion: "governance.waycloak.io/v1", Owner: "maintainers", ReviewedAt: "2026-08-29", ReviewAfter: "2026-11-27",
		Coverage:      coverage{DirectGoModules: "go.mod", ShippedHelperBinaries: []string{}, BaseImages: []string{}, HelmDependencies: []string{}, GeneratedClients: []string{}},
		RuntimeBudget: runtimeBudget{AgentRSSLimitBytes: 2, MeasuredAgentRSSMaxBytes: 1, BinaryDeltaLimitBytes: 1, BinaryMeasurements: map[string]binary{"x": {BeforeBytes: 1, AfterBytes: 1}}, Evidence: "evidence"},
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/x\n\nrequire (\n example.test/direct v1.0.0\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	qualified := dependency{ID: "example.test/direct", Kind: "go-module", Version: "v1.0.0", LatestObserved: "v1.1.0", Source: "source", License: "MIT", SecurityPolicy: "security", Maintenance: "active", Scopes: []string{"runtime"}, SBOMEvidence: "sbom", ProvenanceEvidence: "provenance", Reproducibility: "repro", VulnerabilityGate: "vuln", Compatibility: "compat", RuntimeCost: "cost"}
	base.Dependencies = []dependency{qualified}
	base.ReleaseArtifacts = validArtifacts()
	if err := verifyInventory(base, root, now); err == nil || !strings.Contains(err.Error(), "without an owned exception") {
		t.Fatalf("error = %v", err)
	}
	base.Dependencies[0].Lag = &lag{Owner: "maintainers", Reason: "compatibility", ReviewAfter: "2026-09-30"}
	base.RuntimeBudget.BinaryMeasurements["x"] = binary{BeforeBytes: 1, AfterBytes: 3}
	if err := verifyInventory(base, root, now); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func validArtifacts() []releaseArtifact {
	names := []string{"chart", "kcl", "replacement-controller", "waycloak-cni", "waycloak-node-agent", "waycloak-gateway-agent", "waycloak-gateway-runtime", "waycloak-qbittorrent-adapter", "gluetun", "pause"}
	result := make([]releaseArtifact, 0, len(names))
	for _, name := range names {
		result = append(result, releaseArtifact{Name: name, IdentityIn: "release-manifest.json", SBOM: "spdx", Provenance: "attestation"})
	}
	return result
}
