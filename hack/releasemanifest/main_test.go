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
	value, err := buildManifest("0.1.0", "https://github.com/example/waycloak", strings.Repeat("b", 40), "https://github.com/example/waycloak/actions/runs/1", map[string]string{"controllerImage": reference, "agentImage": reference, "gatewayManagerImage": reference, "qbittorrentAdapterImage": reference, "helmChart": reference})
	if err != nil {
		t.Fatal(err)
	}
	if value.Artifacts["controllerImage"].Digest != "sha256:"+strings.Repeat("a", 64) || value.Artifacts["qbittorrentAdapterImage"].Reference != reference || value.Security.TestedGluetun != testedGluetun || value.Compatibility.CRDStorageVersion != "v1alpha1" {
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
	for _, name := range []string{"controllerImage", "agentImage", "gatewayManagerImage", "qbittorrentAdapterImage", "helmChart"} {
		if !required[name] {
			t.Fatalf("release manifest schema does not require %s", name)
		}
	}
}

func TestBuildManifestRejectsMutableArtifact(t *testing.T) {
	_, err := buildManifest("0.1.0", "https://github.com/example/waycloak", strings.Repeat("b", 40), "https://github.com/example/waycloak/actions/runs/1", map[string]string{"controllerImage": "ghcr.io/example/controller:latest"})
	if err == nil {
		t.Fatal("mutable artifact was accepted")
	}
}
