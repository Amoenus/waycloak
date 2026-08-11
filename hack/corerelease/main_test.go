// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
)

func TestRunProducesDeterministicLoadableCoreManifest(t *testing.T) {
	firstArguments := validArguments()
	secondArguments := append([]string(nil), firstArguments[:4]...)
	for index := len(firstArguments) - 2; index >= 4; index -= 2 {
		secondArguments = append(secondArguments, firstArguments[index], firstArguments[index+1])
	}
	first, second := &bytes.Buffer{}, &bytes.Buffer{}
	if err := run(firstArguments, first); err != nil {
		t.Fatal(err)
	}
	if err := run(secondArguments, second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("argument ordering changed release manifest:\n%s\n%s", first, second)
	}
	path := filepath.Join(t.TempDir(), "release-manifest.json")
	if err := os.WriteFile(path, first.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := waycloakctl.LoadReleaseManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "v1.0.0-beta.1" || len(manifest.Images) != 8 || manifest.ManifestDigest == "" {
		t.Fatalf("unexpected generated manifest: %#v", manifest)
	}
}

func TestRunContinuesToProduceLoadableCoreOnlyManifestForRollback(t *testing.T) {
	output := &bytes.Buffer{}
	if err := run(validCoreArguments(), output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release-manifest.json")
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := waycloakctl.LoadReleaseManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Images) != 6 {
		t.Fatalf("Core-only rollback manifest contains %d images", len(manifest.Images))
	}
}

func TestRunRejectsMissingExtraDuplicateAndMutableIdentities(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		wanted    string
	}{
		{name: "missing", arguments: withoutImage(validArguments(), "pause"), wanted: "required artifacts"},
		{name: "partial port forwarding", arguments: validArguments()[:len(validArguments())-2], wanted: "known port-forward artifacts"},
		{name: "extra", arguments: append(validArguments(), "--image", exactImage("other", "other", "9")), wanted: "required artifacts"},
		{name: "duplicate", arguments: append(validArguments(), "--image", exactImage("pause", "pause-copy", "9")), wanted: "duplicated"},
		{name: "tag", arguments: replaceArgument(validArguments(), "--chart", "oci://registry.invalid/charts/waycloak:v1"), wanted: "repository@sha256"},
		{name: "uppercase", arguments: replaceArgument(validArguments(), "--chart", "oci://registry.invalid/charts/waycloak@sha256:"+strings.Repeat("A", 64)), wanted: "lowercase"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := run(test.arguments, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), test.wanted) {
				t.Fatalf("error = %v, want %q", err, test.wanted)
			}
		})
	}
}

func validArguments() []string {
	return append(validCoreArguments(),
		"--image", exactImage("waycloak-gateway-runtime", "waycloak-gateway-runtime", "2"),
		"--image", exactImage("waycloak-qbittorrent-adapter", "waycloak-qbittorrent-adapter", "3"),
	)
}

func validCoreArguments() []string {
	return []string{
		"--version", "v1.0.0-beta.1",
		"--chart", exactArtifact("oci://registry.invalid/charts/waycloak", "a"),
		"--image", exactImage("replacement-controller", "replacement-controller", "b"),
		"--image", exactImage("waycloak-cni", "waycloak-cni", "c"),
		"--image", exactImage("waycloak-node-agent", "waycloak-node-agent", "d"),
		"--image", exactImage("waycloak-gateway-agent", "waycloak-gateway-agent", "e"),
		"--image", exactImage("gluetun", "gluetun", "f"),
		"--image", exactImage("pause", "pause", "1"),
	}
}

func withoutImage(arguments []string, name string) []string {
	result := make([]string, 0, len(arguments)-2)
	for index := 0; index < len(arguments); index++ {
		if arguments[index] == "--image" && index+1 < len(arguments) && strings.HasPrefix(arguments[index+1], name+"=") {
			index++
			continue
		}
		result = append(result, arguments[index])
	}
	return result
}

func exactImage(name, repository, character string) string {
	return name + "=" + exactArtifact("registry.invalid/"+repository, character)
}

func exactArtifact(repository, character string) string {
	return repository + "@sha256:" + strings.Repeat(character, 64)
}

func replaceArgument(arguments []string, name, value string) []string {
	result := append([]string(nil), arguments...)
	for index := range result {
		if result[index] == name {
			result[index+1] = value
			return result
		}
	}
	return result
}
