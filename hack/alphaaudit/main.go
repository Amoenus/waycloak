// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Command alphaaudit proves that shipped replacement surfaces contain no alpha
// API, admission-injection, sidecar, or persisted-handshake contract.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const defaultPolicy = "docs/implementation/alpha-removal-completion.json"

type policy struct {
	SchemaVersion    int      `json:"schemaVersion"`
	AuditedPaths     []string `json:"auditedPaths"`
	ExemptPaths      []string `json:"exemptPaths"`
	ForbiddenPaths   []string `json:"forbiddenPaths"`
	ForbiddenMarkers []string `json:"forbiddenMarkers"`
}

func main() {
	policyPath := flag.String("policy", defaultPolicy, "path to the completed alpha-removal policy")
	flag.Parse()
	if err := run(context.Background(), ".", *policyPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, root, policyPath string) error {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(policyPath)))
	if err != nil {
		return fmt.Errorf("read policy: %w", err)
	}
	var value policy
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode policy: %w", err)
	}
	paths, err := trackedPaths(ctx, root)
	if err != nil {
		return err
	}
	return audit(root, value, paths)
}

func trackedPaths(ctx context.Context, root string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list repository files: %w", err)
	}
	var paths []string
	for _, path := range strings.Split(string(output), "\x00") {
		if path == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", path, err)
		}
		paths = append(paths, filepath.ToSlash(path))
	}
	return paths, nil
}

func audit(root string, value policy, paths []string) error {
	if value.SchemaVersion != 1 || len(value.AuditedPaths) == 0 || len(value.ForbiddenPaths) == 0 || len(value.ForbiddenMarkers) == 0 {
		return errors.New("alpha-removal policy must declare schemaVersion 1 and non-empty audited paths, forbidden paths, and markers")
	}
	compiled := make([]*regexp.Regexp, 0, len(value.ForbiddenMarkers))
	for _, marker := range value.ForbiddenMarkers {
		expression, err := regexp.Compile(marker)
		if err != nil {
			return fmt.Errorf("compile forbidden marker %q: %w", marker, err)
		}
		compiled = append(compiled, expression)
	}
	var violations []string
	for _, path := range paths {
		if covered(path, value.ForbiddenPaths) {
			violations = append(violations, path+" (removed alpha path exists)")
		}
		if !covered(path, value.AuditedPaths) || covered(path, value.ExemptPaths) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return fmt.Errorf("read audited file %s: %w", path, err)
		}
		for index, expression := range compiled {
			if expression.Find(data) != nil {
				violations = append(violations, fmt.Sprintf("%s (forbidden marker %d)", path, index+1))
				break
			}
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		return fmt.Errorf("replacement contains alpha runtime contracts:\n  %s", strings.Join(violations, "\n  "))
	}
	fmt.Printf("replacement alpha-removal audit passed: %d repository files\n", len(paths))
	return nil
}

func covered(path string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(pattern)
		if strings.HasSuffix(pattern, "/**") {
			if strings.HasPrefix(path, strings.TrimSuffix(pattern, "**")) {
				return true
			}
		} else if path == pattern {
			return true
		}
	}
	return false
}
