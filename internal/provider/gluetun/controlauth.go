// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var authenticatedControlRoutes = []string{"GET /v1/dns/status", "GET /v1/publicip/ip", "GET /v1/portforward"}

// WriteControlAuthentication derives a Pod-local Gluetun control identity from
// the existing gateway-runtime private key. The source key is never copied to
// the shared control volume and neither value is logged.
func WriteControlAuthentication(sourceKeyPath, configPath, apiKeyPath string) error {
	if sourceKeyPath == "" || configPath == "" || apiKeyPath == "" || configPath == apiKeyPath {
		return errors.New("exact Gluetun control credential paths are required")
	}
	source, err := os.ReadFile(sourceKeyPath)
	if err != nil || len(source) == 0 {
		return errors.New("read gateway runtime control identity source")
	}
	digestInput := append([]byte("waycloak/gluetun-control/v1\x00"), source...)
	digest := sha256.Sum256(digestInput)
	apiKey := hex.EncodeToString(digest[:])
	config := fmt.Sprintf("[[roles]]\nname = %q\nroutes = [%q, %q, %q]\nauth = %q\napikey = %q\n",
		"waycloak-gateway", authenticatedControlRoutes[0], authenticatedControlRoutes[1], authenticatedControlRoutes[2], "apikey", apiKey)
	for _, path := range []string{configPath, apiKeyPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	}
	if err := writeCredentialFile(configPath, []byte(config)); err != nil {
		return err
	}
	return writeCredentialFile(apiKeyPath, []byte(apiKey+"\n"))
}

func writeCredentialFile(path string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".waycloak-gluetun-control-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o400); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
