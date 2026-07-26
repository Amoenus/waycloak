// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Command alphaaudit verifies that every tracked alpha contract is present in
// the reviewed removal ledger and rejects newly introduced alpha surfaces.
package main

import (
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

const defaultInventory = "docs/implementation/alpha-removal-inventory.json"

var allowedClassifications = map[string]bool{
	"invariant_redesign":           true,
	"implementation_delete":        true,
	"independent_product_decision": true,
}

var artifactKinds = []string{"code", "chart", "generated", "tests", "docs"}

type inventory struct {
	SchemaVersion   int      `json:"schemaVersion"`
	BlockedIssue    int      `json:"blockedIssue"`
	Markers         []string `json:"markers"`
	KnownAlphaPaths []string `json:"knownAlphaPaths"`
	ExemptPaths     []string `json:"exemptPaths"`
	Entries         []entry  `json:"entries"`
}

type entry struct {
	ID             string              `json:"id"`
	Classification string              `json:"classification"`
	RemovalIssue   int                 `json:"removalIssue"`
	Contracts      []string            `json:"contracts"`
	Artifacts      map[string][]string `json:"artifacts"`
}

func main() {
	inventoryPath := flag.String("inventory", defaultInventory, "path to the alpha removal inventory")
	flag.Parse()
	if err := run(".", *inventoryPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root, inventoryPath string) error {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(inventoryPath)))
	if err != nil {
		return fmt.Errorf("read inventory: %w", err)
	}
	var inv inventory
	if err := json.Unmarshal(data, &inv); err != nil {
		return fmt.Errorf("decode inventory: %w", err)
	}
	paths, err := trackedPaths(root)
	if err != nil {
		return err
	}
	return audit(root, inv, paths)
}

func trackedPaths(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	parts := strings.Split(string(output), "\x00")
	paths := make([]string, 0, len(parts))
	for _, path := range parts {
		if path == "" {
			continue
		}
		_, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("inspect tracked file %s: %w", path, statErr)
		}
		paths = append(paths, filepath.ToSlash(path))
	}
	return paths, nil
}

func audit(root string, inv inventory, paths []string) error {
	if inv.SchemaVersion != 1 {
		return fmt.Errorf("unsupported inventory schemaVersion %d", inv.SchemaVersion)
	}
	if inv.BlockedIssue != 127 {
		return fmt.Errorf("inventory must block issue 127, got %d", inv.BlockedIssue)
	}
	if len(inv.Markers) == 0 || len(inv.KnownAlphaPaths) == 0 || len(inv.Entries) == 0 {
		return errors.New("inventory markers, knownAlphaPaths, and entries must not be empty")
	}
	patterns := append([]string(nil), inv.ExemptPaths...)
	seenIDs := map[string]bool{}
	for _, item := range inv.Entries {
		if item.ID == "" || seenIDs[item.ID] {
			return fmt.Errorf("inventory entry ID %q is empty or duplicated", item.ID)
		}
		seenIDs[item.ID] = true
		if !allowedClassifications[item.Classification] {
			return fmt.Errorf("entry %s has unsupported classification %q", item.ID, item.Classification)
		}
		if item.RemovalIssue == 0 || len(item.Contracts) == 0 {
			return fmt.Errorf("entry %s must name contracts and a removal issue", item.ID)
		}
		for _, kind := range artifactKinds {
			listed, ok := item.Artifacts[kind]
			if !ok {
				return fmt.Errorf("entry %s does not declare %s artifacts", item.ID, kind)
			}
			patterns = append(patterns, listed...)
		}
		for kind := range item.Artifacts {
			if !contains(artifactKinds, kind) {
				return fmt.Errorf("entry %s has unknown artifact kind %q", item.ID, kind)
			}
		}
	}
	for _, pattern := range patterns {
		if !matchesAny(pattern, paths) {
			return fmt.Errorf("inventory path %q does not match a tracked file", pattern)
		}
	}
	knownAlphaPaths := make(map[string]bool, len(inv.KnownAlphaPaths))
	for _, path := range inv.KnownAlphaPaths {
		if strings.Contains(path, "*") || knownAlphaPaths[path] {
			return fmt.Errorf("knownAlphaPaths entry %q is a pattern or duplicate", path)
		}
		if !matchesAny(path, paths) {
			return fmt.Errorf("known alpha path %q is not tracked", path)
		}
		knownAlphaPaths[path] = true
	}
	compiled := make([]*regexp.Regexp, 0, len(inv.Markers))
	for _, marker := range inv.Markers {
		re, err := regexp.Compile(marker)
		if err != nil {
			return fmt.Errorf("compile marker %q: %w", marker, err)
		}
		compiled = append(compiled, re)
	}
	var unknown []string
	observedKnown := make(map[string]bool, len(knownAlphaPaths))
	for _, path := range paths {
		if covered(path, inv.ExemptPaths) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return fmt.Errorf("read tracked file %s: %w", path, err)
		}
		for index, re := range compiled {
			if re.FindString(path) != "" || re.Find(data) != nil {
				observedKnown[path] = true
				if !covered(path, patterns) || !knownAlphaPaths[path] {
					unknown = append(unknown, fmt.Sprintf("%s (marker %d: %s)", path, index+1, inv.Markers[index]))
				}
				break
			}
		}
	}
	for path := range knownAlphaPaths {
		if !observedKnown[path] {
			unknown = append(unknown, fmt.Sprintf("%s (listed path no longer contains an alpha marker)", path))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unlisted alpha artifacts:\n  %s", strings.Join(unknown, "\n  "))
	}
	fmt.Printf("alpha removal inventory verified: %d entries, %d tracked files\n", len(inv.Entries), len(paths))
	return nil
}

func matchesAny(pattern string, paths []string) bool {
	for _, path := range paths {
		if pathMatches(pattern, path) {
			return true
		}
	}
	return false
}

func covered(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if pathMatches(pattern, path) {
			return true
		}
	}
	return false
}

func pathMatches(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	if strings.HasSuffix(pattern, "/**") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "**"))
	}
	return path == pattern
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
