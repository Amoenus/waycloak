// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	digestPattern  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

func main() {
	chartDirectory := flag.String("chart", "charts/waycloak", "chart directory")
	version := flag.String("version", "", "semantic release version without a v prefix")
	controller := flag.String("controller", "", "controller image digest reference")
	agent := flag.String("agent", "", "agent image digest reference")
	gatewayManager := flag.String("gateway-manager", "", "gateway-manager image digest reference")
	flag.Parse()
	if err := prepare(*chartDirectory, *version, map[string]string{"controller": *controller, "agent": *agent, "gatewayManager": *gatewayManager}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepare(chartDirectory, version string, images map[string]string) error {
	if !versionPattern.MatchString(version) {
		return errors.New("version must be semantic and must not include a v prefix")
	}
	chartPath := filepath.Join(chartDirectory, "Chart.yaml")
	chart, err := os.ReadFile(chartPath)
	if err != nil {
		return fmt.Errorf("read Chart.yaml: %w", err)
	}
	updatedChart, err := replaceExactly(string(chart), regexp.MustCompile(`(?m)^version: .+$`), "version: "+version)
	if err != nil {
		return err
	}
	updatedChart, err = replaceExactly(updatedChart, regexp.MustCompile(`(?m)^appVersion: .+$`), "appVersion: "+version)
	if err != nil {
		return err
	}
	valuesPath := filepath.Join(chartDirectory, "values.yaml")
	values, err := os.ReadFile(valuesPath)
	if err != nil {
		return fmt.Errorf("read values.yaml: %w", err)
	}
	updatedValues := string(values)
	for _, name := range []string{"controller", "agent", "gatewayManager"} {
		repository, digest, splitErr := splitDigestReference(images[name])
		if splitErr != nil {
			return fmt.Errorf("%s image: %w", name, splitErr)
		}
		pattern := regexp.MustCompile(`(?m)(^  ` + regexp.QuoteMeta(name) + `:\r?\n    repository: )[^\r\n]+(\r?\n    digest: )[^\r\n]+`)
		replacement := `${1}` + repository + `${2}"` + digest + `"`
		updatedValues, err = replaceExactly(updatedValues, pattern, replacement)
		if err != nil {
			return fmt.Errorf("update %s image: %w", name, err)
		}
	}
	if err := os.WriteFile(chartPath, []byte(updatedChart), 0o644); err != nil {
		return fmt.Errorf("write Chart.yaml: %w", err)
	}
	if err := os.WriteFile(valuesPath, []byte(updatedValues), 0o644); err != nil {
		return fmt.Errorf("write values.yaml: %w", err)
	}
	return nil
}

func splitDigestReference(reference string) (string, string, error) {
	index := strings.LastIndex(reference, "@")
	if index < 1 || index == len(reference)-1 {
		return "", "", errors.New("reference must use repository@sha256:digest")
	}
	repository, digest := reference[:index], reference[index+1:]
	if strings.Contains(repository, "@") || !digestPattern.MatchString(digest) {
		return "", "", errors.New("reference must use one lowercase SHA-256 digest")
	}
	return repository, digest, nil
}

func replaceExactly(input string, pattern *regexp.Regexp, replacement string) (string, error) {
	if len(pattern.FindAllStringIndex(input, -1)) != 1 {
		return "", fmt.Errorf("expected exactly one match for %q", pattern)
	}
	return pattern.ReplaceAllString(input, replacement), nil
}
