// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"
)

func TestBuildManifestTiesImmutableArtifacts(t *testing.T) {
	reference := "ghcr.io/example/waycloak@sha256:" + strings.Repeat("a", 64)
	value, err := buildManifest("0.1.0", "https://github.com/example/waycloak", strings.Repeat("b", 40), "https://github.com/example/waycloak/actions/runs/1", map[string]string{"controllerImage": reference, "agentImage": reference, "gatewayManagerImage": reference, "helmChart": reference})
	if err != nil {
		t.Fatal(err)
	}
	if value.Artifacts["controllerImage"].Digest != "sha256:"+strings.Repeat("a", 64) || value.Security.TestedGluetun != testedGluetun || value.Compatibility.CRDStorageVersion != "v1alpha1" {
		t.Fatalf("manifest = %#v", value)
	}
}

func TestBuildManifestRejectsMutableArtifact(t *testing.T) {
	_, err := buildManifest("0.1.0", "https://github.com/example/waycloak", strings.Repeat("b", 40), "https://github.com/example/waycloak/actions/runs/1", map[string]string{"controllerImage": "ghcr.io/example/controller:latest"})
	if err == nil {
		t.Fatal("mutable artifact was accepted")
	}
}
