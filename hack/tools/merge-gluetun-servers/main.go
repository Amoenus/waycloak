// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type providerManifest struct {
	Filepath string `json:"filepath"`
}

type providerData struct {
	Version   int               `json:"version"`
	Timestamp int64             `json:"timestamp"`
	Servers   []json.RawMessage `json:"servers"`
}

func main() {
	basePath := flag.String("base", "", "combined Gluetun servers.json to update")
	manifestPath := flag.String("manifest", "", "gluetun-servers manifest.json")
	serversDirectory := flag.String("servers-dir", "", "directory containing provider JSON files")
	provider := flag.String("provider", "", "provider key to import")
	outputPath := flag.String("output", "", "deterministic combined output path")
	flag.Parse()

	if err := merge(*basePath, *manifestPath, *serversDirectory, *provider, *outputPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func merge(basePath, manifestPath, serversDirectory, provider, outputPath string) error {
	if basePath == "" || manifestPath == "" || serversDirectory == "" || provider == "" || outputPath == "" {
		return errors.New("base, manifest, servers-dir, provider, and output are required")
	}

	base, err := readObject(basePath)
	if err != nil {
		return fmt.Errorf("read combined Gluetun servers: %w", err)
	}
	if err := requireVersion(base, 1, "combined Gluetun servers"); err != nil {
		return err
	}
	manifest, err := readObject(manifestPath)
	if err != nil {
		return fmt.Errorf("read gluetun-servers manifest: %w", err)
	}
	if err := requireVersion(manifest, 1, "gluetun-servers manifest"); err != nil {
		return err
	}

	manifestEntryRaw, ok := manifest[provider]
	if !ok {
		return fmt.Errorf("provider %q is absent from gluetun-servers manifest", provider)
	}
	var manifestEntry providerManifest
	if err := json.Unmarshal(manifestEntryRaw, &manifestEntry); err != nil {
		return fmt.Errorf("decode provider %q manifest entry: %w", provider, err)
	}
	wantFilename := provider + ".json"
	if filepath.Base(manifestEntry.Filepath) != wantFilename {
		return fmt.Errorf("provider %q manifest filepath is not %q", provider, wantFilename)
	}

	oldRaw, ok := base[provider]
	if !ok {
		return fmt.Errorf("provider %q is absent from combined Gluetun servers", provider)
	}
	oldData, err := decodeProvider(oldRaw)
	if err != nil {
		return fmt.Errorf("decode existing provider %q data: %w", provider, err)
	}

	providerPath := filepath.Join(serversDirectory, wantFilename)
	newObject, err := readObject(providerPath)
	if err != nil {
		return fmt.Errorf("read provider %q data: %w", provider, err)
	}
	newRaw, err := json.Marshal(newObject)
	if err != nil {
		return fmt.Errorf("encode provider %q data: %w", provider, err)
	}
	newData, err := decodeProvider(newRaw)
	if err != nil {
		return fmt.Errorf("decode replacement provider %q data: %w", provider, err)
	}
	if newData.Version != oldData.Version {
		return fmt.Errorf("provider %q schema version changed from %d to %d", provider, oldData.Version, newData.Version)
	}
	if newData.Timestamp <= oldData.Timestamp {
		return fmt.Errorf("provider %q data timestamp %d is not newer than %d", provider, newData.Timestamp, oldData.Timestamp)
	}
	if len(newData.Servers) == 0 {
		return fmt.Errorf("provider %q replacement contains no servers", provider)
	}

	base[provider] = newRaw
	encoded, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return fmt.Errorf("encode combined Gluetun servers: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(outputPath, encoded, 0o644); err != nil {
		return fmt.Errorf("write combined Gluetun servers: %w", err)
	}
	return nil
}

func readObject(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func requireVersion(object map[string]json.RawMessage, want int, description string) error {
	var got int
	if err := json.Unmarshal(object["version"], &got); err != nil {
		return fmt.Errorf("decode %s version: %w", description, err)
	}
	if got != want {
		return fmt.Errorf("%s version is %d, want %d", description, got, want)
	}
	return nil
}

func decodeProvider(raw json.RawMessage) (providerData, error) {
	var data providerData
	if err := json.Unmarshal(raw, &data); err != nil {
		return providerData{}, err
	}
	if data.Version < 1 || data.Timestamp < 1 || len(data.Servers) == 0 {
		return providerData{}, errors.New("provider data is incomplete")
	}
	return data, nil
}
