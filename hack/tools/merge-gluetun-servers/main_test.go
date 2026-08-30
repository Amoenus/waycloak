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

func TestMergeReplacesOnlyExactNewerProviderData(t *testing.T) {
	directory := t.TempDir()
	serversDirectory := filepath.Join(directory, "servers")
	if err := os.Mkdir(serversDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	basePath := writeFixture(t, directory, "base.json", `{"version":1,"other":{"version":1,"timestamp":4,"servers":[{"name":"keep"}]},"protonvpn":{"version":4,"timestamp":5,"servers":[{"name":"old"}]}}`)
	manifestPath := writeFixture(t, directory, "manifest.json", `{"version":1,"protonvpn":{"filepath":"/gluetun/servers/protonvpn.json"}}`)
	writeFixture(t, serversDirectory, "protonvpn.json", `{"version":4,"timestamp":6,"servers":[{"name":"new"},{"name":"newer"}]}`)
	outputPath := filepath.Join(directory, "output.json")

	if err := merge(basePath, manifestPath, serversDirectory, "protonvpn", outputPath); err != nil {
		t.Fatal(err)
	}
	result, err := readObject(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var other providerData
	if err := json.Unmarshal(result["other"], &other); err != nil || other.Timestamp != 4 {
		t.Fatalf("unrelated provider changed: %#v, %v", other, err)
	}
	updated, err := decodeProvider(result["protonvpn"])
	if err != nil || updated.Timestamp != 6 || len(updated.Servers) != 2 {
		t.Fatalf("replacement provider = %#v, %v", updated, err)
	}
	first, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := merge(basePath, manifestPath, serversDirectory, "protonvpn", outputPath); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || !strings.HasSuffix(string(second), "\n") {
		t.Fatal("merged output is not deterministic or lacks a final newline")
	}
}

func TestMergeRejectsIncompatibleOrNonNewerData(t *testing.T) {
	for _, test := range []struct {
		name        string
		replacement string
		want        string
	}{
		{name: "schema", replacement: `{"version":5,"timestamp":6,"servers":[{}]}`, want: "schema version changed"},
		{name: "timestamp", replacement: `{"version":4,"timestamp":5,"servers":[{}]}`, want: "is not newer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			serversDirectory := filepath.Join(directory, "servers")
			if err := os.Mkdir(serversDirectory, 0o755); err != nil {
				t.Fatal(err)
			}
			basePath := writeFixture(t, directory, "base.json", `{"version":1,"protonvpn":{"version":4,"timestamp":5,"servers":[{}]}}`)
			manifestPath := writeFixture(t, directory, "manifest.json", `{"version":1,"protonvpn":{"filepath":"/gluetun/servers/protonvpn.json"}}`)
			writeFixture(t, serversDirectory, "protonvpn.json", test.replacement)
			err := merge(basePath, manifestPath, serversDirectory, "protonvpn", filepath.Join(directory, "output.json"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("merge error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMergeRejectsCombinedDatabaseSchemaChange(t *testing.T) {
	directory := t.TempDir()
	serversDirectory := filepath.Join(directory, "servers")
	if err := os.Mkdir(serversDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	basePath := writeFixture(t, directory, "base.json", `{"version":2,"protonvpn":{"version":4,"timestamp":5,"servers":[{}]}}`)
	manifestPath := writeFixture(t, directory, "manifest.json", `{"version":1,"protonvpn":{"filepath":"/gluetun/servers/protonvpn.json"}}`)
	writeFixture(t, serversDirectory, "protonvpn.json", `{"version":4,"timestamp":6,"servers":[{}]}`)

	err := merge(basePath, manifestPath, serversDirectory, "protonvpn", filepath.Join(directory, "output.json"))
	if err == nil || !strings.Contains(err.Error(), "combined Gluetun servers version is 2, want 1") {
		t.Fatalf("merge error = %v", err)
	}
}

func writeFixture(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
