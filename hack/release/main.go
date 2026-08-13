// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Command release assembles the publisher-owned exact Waycloak artifact
// inventory. It is not an installation fallback and never discovers or
// substitutes artifact identities.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
)

type repeatedFlag []string

func (values *repeatedFlag) String() string { return strings.Join(*values, ",") }
func (values *repeatedFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var version, chartValue, kclValue string
	var imageValues, profiles repeatedFlag
	flags.StringVar(&version, "version", "", "immutable release version")
	flags.StringVar(&chartValue, "chart", "", "exact chart repository@sha256:digest")
	flags.StringVar(&kclValue, "kcl", "", "exact KCL module repository@sha256:digest")
	flags.Var(&imageValues, "image", "required name=repository@sha256:digest; repeat once per release image")
	flags.Var(&profiles, "profile", "conformance profile; defaults to Core-v1")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || version == "" {
		return errors.New("--version, --chart, and the exact required --image values are required")
	}
	chart, err := parseArtifact(chartValue)
	if err != nil {
		return fmt.Errorf("chart identity: %w", err)
	}
	kcl, err := parseArtifact(kclValue)
	if err != nil {
		return fmt.Errorf("KCL module identity: %w", err)
	}
	images := make(map[string]waycloakctl.Artifact, len(imageValues))
	for _, value := range imageValues {
		name, rawArtifact, found := strings.Cut(value, "=")
		if !found || name == "" {
			return fmt.Errorf("image identity %q must be name=repository@sha256:digest", value)
		}
		if _, duplicate := images[name]; duplicate {
			return fmt.Errorf("image identity %q is duplicated", name)
		}
		artifact, parseErr := parseArtifact(rawArtifact)
		if parseErr != nil {
			return fmt.Errorf("image identity %q: %w", name, parseErr)
		}
		images[name] = artifact
	}
	if len(profiles) == 0 {
		profiles = []string{"networking.waycloak.io/Core-v1"}
	}
	sort.Strings(profiles)
	manifest := waycloakctl.ReleaseManifest{
		APIVersion: "release.waycloak.io/v1", Version: version,
		Chart: chart, KCL: &kcl, Images: images, Profiles: profiles,
	}
	manifest.ManifestDigest, err = manifest.IdentityDigest()
	if err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(manifest)
}

func parseArtifact(value string) (waycloakctl.Artifact, error) {
	separator := strings.LastIndex(value, "@sha256:")
	if separator <= 0 {
		return waycloakctl.Artifact{}, errors.New("identity must be repository@sha256:digest")
	}
	repository, digest := value[:separator], value[separator+1:]
	if strings.Contains(repository, "@") || len(digest) != len("sha256:")+64 {
		return waycloakctl.Artifact{}, errors.New("identity must contain one exact SHA-256 digest")
	}
	for _, character := range strings.TrimPrefix(digest, "sha256:") {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return waycloakctl.Artifact{}, errors.New("digest must use 64 lowercase hexadecimal characters")
		}
	}
	return waycloakctl.Artifact{Repository: repository, Digest: digest}, nil
}
