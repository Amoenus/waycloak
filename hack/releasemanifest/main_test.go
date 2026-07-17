// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildManifestTiesImmutableArtifacts(t *testing.T) {
	reference := "ghcr.io/example/waycloak@sha256:" + strings.Repeat("a", 64)
	value, err := buildManifest("0.2.0", "https://github.com/example/waycloak", strings.Repeat("b", 40), "https://github.com/example/waycloak/actions/runs/1", completeReferences(reference))
	if err != nil {
		t.Fatal(err)
	}
	if value.SchemaVersion != "1.3.0" || value.Artifacts["controllerImage"].Digest != "sha256:"+strings.Repeat("a", 64) || value.Artifacts["qbittorrentAdapterImage"].Reference != reference || value.Artifacts["bitmagnetAdapterImage"].Reference != reference || value.Artifacts["kclModule"].Reference != reference || value.Security.TestedGluetun != testedGluetun || value.Compatibility.CRDStorageVersion != "v1alpha1" || value.Compatibility.WorkloadAdapterProtocol != "networking.waycloak.io/adapter/v1alpha1" || value.Compatibility.WorkloadAdapterConformanceKit != "workload-adapter-kit-v1alpha1.tar.gz" || value.Compatibility.ReferenceAdapters["qbittorrent"] != ">=5.2.3 <6.0.0" || value.Compatibility.ReferenceAdapters["bitmagnet"] != ">=0.10.1-beta.1 <1.0.0" {
		t.Fatalf("manifest = %#v", value)
	}
}

func TestSchemaRequiresEveryReleasedArtifact(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "docs", "release", "manifest.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Artifacts struct {
				Required []string `json:"required"`
			} `json:"artifacts"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(source, &schema); err != nil {
		t.Fatal(err)
	}
	required := make(map[string]bool, len(schema.Properties.Artifacts.Required))
	for _, name := range schema.Properties.Artifacts.Required {
		required[name] = true
	}
	for _, name := range requiredArtifacts {
		if !required[name] {
			t.Fatalf("release manifest schema does not require %s", name)
		}
	}
}

func TestBuildManifestRejectsMutableArtifact(t *testing.T) {
	references := completeReferences("ghcr.io/example/waycloak@sha256:" + strings.Repeat("a", 64))
	references["controllerImage"] = "ghcr.io/example/controller:latest"
	_, err := buildManifest("0.2.0", "https://github.com/example/waycloak", strings.Repeat("b", 40), "https://github.com/example/waycloak/actions/runs/1", references)
	if err == nil {
		t.Fatal("mutable artifact was accepted")
	}
}

func TestBuildManifestRejectsMissingKCLModule(t *testing.T) {
	references := completeReferences("ghcr.io/example/waycloak@sha256:" + strings.Repeat("a", 64))
	delete(references, "kclModule")
	_, err := buildManifest("0.2.0", "https://github.com/example/waycloak", strings.Repeat("b", 40), "https://github.com/example/waycloak/actions/runs/1", references)
	if err == nil || !strings.Contains(err.Error(), "kclModule") {
		t.Fatalf("missing KCL module result = %v", err)
	}
}

func completeReferences(reference string) map[string]string {
	references := make(map[string]string, len(requiredArtifacts))
	for _, name := range requiredArtifacts {
		references[name] = reference
	}
	return references
}
