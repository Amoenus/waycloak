// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

func main() {
	version := flag.String("version", "", "semantic release version without a v prefix")
	flag.Parse()
	prerelease, err := isPrerelease(*version)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(prerelease)
}

func isPrerelease(version string) (bool, error) {
	if !versionPattern.MatchString(version) {
		return false, errors.New("version must be semantic and must not include a v prefix")
	}
	return strings.Contains(version, "-"), nil
}
