// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const adapterPlaceholder = "waycloak.invalid/qbittorrent-adapter@sha256:0000000000000000000000000000000000000000000000000000000000000000"

var immutableReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9./:_-]*@sha256:[a-f0-9]{64}\z`)

func main() {
	input := flag.String("input", "examples/qbittorrent/workload.yaml", "source workload manifest")
	output := flag.String("output", "", "rendered workload manifest; defaults to replacing input")
	adapter := flag.String("adapter", "", "immutable qBitTorrent adapter reference")
	flag.Parse()
	if *output == "" {
		*output = *input
	}
	source, err := os.ReadFile(filepath.Clean(*input))
	if err != nil {
		fail(errors.New("read qBitTorrent example workload"))
	}
	rendered, err := resolveAdapter(source, *adapter)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Clean(*output), rendered, 0o644); err != nil {
		fail(errors.New("write qBitTorrent release workload"))
	}
}

func resolveAdapter(source []byte, adapter string) ([]byte, error) {
	if !immutableReference.MatchString(adapter) {
		return nil, errors.New("adapter must be an immutable SHA-256 reference")
	}
	if count := bytes.Count(source, []byte(adapterPlaceholder)); count != 1 {
		return nil, fmt.Errorf("qBitTorrent example must contain the adapter placeholder exactly once, found %d", count)
	}
	return bytes.Replace(source, []byte(adapterPlaceholder), []byte(adapter), 1), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
