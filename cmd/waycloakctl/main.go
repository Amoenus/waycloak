// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
)

var version = "development"

func main() {
	waycloakctl.Version = version
	if err := waycloakctl.Run(context.Background(), os.Args[1:], waycloakctl.Dependencies{Stdout: os.Stdout, Stderr: os.Stderr}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
