// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Command dependencyaudit verifies the checked-in dependency qualification
// inventory and reports upstream drift without changing dependency pins.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultInventory = "dependencies/dependency-inventory.json"

type inventory struct {
	APIVersion       string            `json:"apiVersion"`
	Owner            string            `json:"owner"`
	ReviewedAt       string            `json:"reviewedAt"`
	ReviewAfter      string            `json:"reviewAfter"`
	Coverage         coverage          `json:"coverage"`
	RuntimeBudget    runtimeBudget     `json:"runtimeBudget"`
	Dependencies     []dependency      `json:"dependencies"`
	ReleaseArtifacts []releaseArtifact `json:"releaseArtifacts"`
}

type coverage struct {
	DirectGoModules       string   `json:"directGoModules"`
	ShippedHelperBinaries []string `json:"shippedHelperBinaries"`
	BaseImages            []string `json:"baseImages"`
	HelmDependencies      []string `json:"helmDependencies"`
	GeneratedClients      []string `json:"generatedClients"`
}

type runtimeBudget struct {
	AgentRSSLimitBytes       int64             `json:"agentRSSLimitBytes"`
	MeasuredAgentRSSMaxBytes int64             `json:"measuredAgentRSSMaxBytes"`
	BinaryDeltaLimitBytes    int64             `json:"binaryDeltaLimitBytes"`
	BinaryMeasurements       map[string]binary `json:"binaryMeasurements"`
	Evidence                 string            `json:"evidence"`
}

type binary struct {
	BeforeBytes int64 `json:"beforeBytes"`
	AfterBytes  int64 `json:"afterBytes"`
}

type dependency struct {
	ID                 string         `json:"id"`
	Kind               string         `json:"kind"`
	Version            string         `json:"version"`
	LatestObserved     string         `json:"latestObserved"`
	Source             string         `json:"source"`
	GitHubRepository   string         `json:"githubRepository,omitempty"`
	License            string         `json:"license"`
	SecurityPolicy     string         `json:"securityPolicy"`
	Maintenance        string         `json:"maintenance"`
	Scopes             []string       `json:"scopes"`
	PinReferences      []pinReference `json:"pinReferences,omitempty"`
	SBOMEvidence       string         `json:"sbomEvidence"`
	ProvenanceEvidence string         `json:"provenanceEvidence"`
	Reproducibility    string         `json:"reproducibilityEvidence"`
	VulnerabilityGate  string         `json:"vulnerabilityGate"`
	Compatibility      string         `json:"compatibilityEvidence"`
	RuntimeCost        string         `json:"runtimeCostEvidence"`
	Lag                *lag           `json:"lag,omitempty"`
}

type pinReference struct {
	Path     string `json:"path"`
	Contains string `json:"contains"`
}

type lag struct {
	Owner       string `json:"owner"`
	Reason      string `json:"reason"`
	ReviewAfter string `json:"reviewAfter"`
}

type releaseArtifact struct {
	Name       string `json:"name"`
	IdentityIn string `json:"identityIn"`
	SBOM       string `json:"sbom"`
	Provenance string `json:"provenance"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout, stderr io.Writer, now time.Time) error {
	flags := flag.NewFlagSet("dependencyaudit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inventoryPath := flags.String("inventory", defaultInventory, "dependency inventory path")
	root := flags.String("root", ".", "repository root")
	upstream := flags.Bool("upstream", false, "report upstream version drift without modifying files")
	githubToken := flags.String("github-token", os.Getenv("GITHUB_TOKEN"), "optional GitHub API token")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("dependencyaudit accepts flags only")
	}
	resolvedInventory := filepath.FromSlash(*inventoryPath)
	if !filepath.IsAbs(resolvedInventory) {
		resolvedInventory = filepath.Join(*root, resolvedInventory)
	}
	loaded, err := loadInventory(resolvedInventory)
	if err != nil {
		return err
	}
	if err := verifyInventory(loaded, *root, now); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "dependency inventory verified: %d dependencies, %d release artifacts\n", len(loaded.Dependencies), len(loaded.ReleaseArtifacts))
	if *upstream {
		return reportUpstream(loaded, *root, *githubToken, stdout)
	}
	return nil
}

func loadInventory(path string) (inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return inventory{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result inventory
	if err := decoder.Decode(&result); err != nil {
		return inventory{}, fmt.Errorf("decode dependency inventory: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return inventory{}, errors.New("dependency inventory contains trailing JSON")
	}
	return result, nil
}

func verifyInventory(value inventory, root string, now time.Time) error {
	if value.APIVersion != "governance.waycloak.io/v1" || value.Owner == "" {
		return errors.New("dependency inventory identity is invalid")
	}
	reviewed, err := time.Parse("2006-01-02", value.ReviewedAt)
	if err != nil || reviewed.After(now.Add(24*time.Hour)) {
		return errors.New("dependency inventory reviewedAt is invalid")
	}
	reviewAfter, err := time.Parse("2006-01-02", value.ReviewAfter)
	if err != nil || now.After(reviewAfter.Add(24*time.Hour)) {
		return errors.New("dependency inventory maintenance review is overdue")
	}
	if value.Coverage.DirectGoModules != "go.mod" || value.Coverage.ShippedHelperBinaries == nil || value.Coverage.BaseImages == nil || value.Coverage.HelmDependencies == nil || value.Coverage.GeneratedClients == nil {
		return errors.New("dependency inventory coverage declarations are incomplete")
	}
	if value.RuntimeBudget.AgentRSSLimitBytes <= 0 || value.RuntimeBudget.MeasuredAgentRSSMaxBytes <= 0 || value.RuntimeBudget.MeasuredAgentRSSMaxBytes > value.RuntimeBudget.AgentRSSLimitBytes || value.RuntimeBudget.BinaryDeltaLimitBytes <= 0 || value.RuntimeBudget.Evidence == "" || len(value.RuntimeBudget.BinaryMeasurements) == 0 {
		return errors.New("dependency runtime budget evidence is incomplete or over budget")
	}
	for name, measurement := range value.RuntimeBudget.BinaryMeasurements {
		if name == "" || measurement.BeforeBytes <= 0 || measurement.AfterBytes <= 0 || measurement.AfterBytes-measurement.BeforeBytes > value.RuntimeBudget.BinaryDeltaLimitBytes {
			return fmt.Errorf("binary measurement %q is invalid or exceeds the dependency delta budget", name)
		}
	}
	directModules, err := parseDirectGoModules(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	inventoryModules := map[string]string{}
	seen := map[string]struct{}{}
	for _, item := range value.Dependencies {
		if _, duplicate := seen[item.ID]; item.ID == "" || duplicate {
			return fmt.Errorf("dependency id %q is empty or duplicated", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Kind == "" || item.Version == "" || item.LatestObserved == "" || item.Source == "" || item.License == "" || item.SecurityPolicy == "" || item.Maintenance != "active" || len(item.Scopes) == 0 || item.SBOMEvidence == "" || item.ProvenanceEvidence == "" || item.Reproducibility == "" || item.VulnerabilityGate == "" || item.Compatibility == "" || item.RuntimeCost == "" {
			return fmt.Errorf("dependency %q lacks required qualification evidence", item.ID)
		}
		if item.Version != item.LatestObserved && item.Lag == nil {
			return fmt.Errorf("dependency %q lags %s without an owned exception", item.ID, item.LatestObserved)
		}
		if item.Lag != nil {
			lagReview, parseErr := time.Parse("2006-01-02", item.Lag.ReviewAfter)
			if item.Lag.Owner == "" || item.Lag.Reason == "" || parseErr != nil || now.After(lagReview.Add(24*time.Hour)) {
				return fmt.Errorf("dependency %q has an invalid or overdue lag exception", item.ID)
			}
		}
		for _, pin := range item.PinReferences {
			if pin.Path == "" || pin.Contains == "" {
				return fmt.Errorf("dependency %q has an incomplete pin reference", item.ID)
			}
			data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(pin.Path)))
			if readErr != nil {
				return fmt.Errorf("dependency %q pin %s: %w", item.ID, pin.Path, readErr)
			}
			if !bytes.Contains(data, []byte(pin.Contains)) {
				return fmt.Errorf("dependency %q pin %s does not contain %q", item.ID, pin.Path, pin.Contains)
			}
		}
		if item.Kind == "go-module" {
			inventoryModules[item.ID] = item.Version
		}
	}
	if !equalStringMap(directModules, inventoryModules) {
		return fmt.Errorf("direct Go module inventory does not exactly match go.mod: go.mod=%v inventory=%v", sortedPairs(directModules), sortedPairs(inventoryModules))
	}
	if len(value.ReleaseArtifacts) != 10 {
		return errors.New("release artifact dependency inventory must cover chart, KCL, and eight images")
	}
	for _, artifact := range value.ReleaseArtifacts {
		if artifact.Name == "" || artifact.IdentityIn != "release-manifest.json" || artifact.SBOM == "" || artifact.Provenance == "" {
			return fmt.Errorf("release artifact %q has incomplete exact-artifact evidence", artifact.Name)
		}
	}
	return nil
}

func parseDirectGoModules(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := map[string]string{}
	inBlock := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "require (" {
			inBlock = true
			continue
		}
		if inBlock && line == ")" {
			inBlock = false
			continue
		}
		if !inBlock || line == "" || strings.Contains(line, "// indirect") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			result[fields[0]] = fields[1]
		}
	}
	return result, scanner.Err()
}

func reportUpstream(value inventory, root, token string, output io.Writer) error {
	updates, err := goModuleUpdates(root)
	if err != nil {
		return err
	}
	dependencies := make(map[string]dependency, len(value.Dependencies))
	for _, item := range value.Dependencies {
		dependencies[item.ID] = item
	}
	for _, update := range updates {
		item := dependencies[update[0]]
		if item.Lag != nil && item.LatestObserved == update[2] {
			fmt.Fprintf(output, "ACCEPTED-LAG go-module %s %s -> %s; owner=%s reviewAfter=%s\n", update[0], update[1], update[2], item.Lag.Owner, item.Lag.ReviewAfter)
			continue
		}
		fmt.Fprintf(output, "UPDATE go-module %s %s -> %s\n", update[0], update[1], update[2])
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for _, item := range value.Dependencies {
		if item.GitHubRepository == "" || !strings.HasPrefix(item.Version, "v") {
			continue
		}
		request, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+item.GitHubRepository+"/releases/latest", nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("query %s: %w", item.GitHubRepository, err)
		}
		var latest struct {
			TagName string `json:"tag_name"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&latest)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil {
			return fmt.Errorf("query %s latest release: HTTP %d", item.GitHubRepository, response.StatusCode)
		}
		if latest.TagName != "" && latest.TagName != item.LatestObserved {
			fmt.Fprintf(output, "REVIEW %s inventory observed %s; upstream now reports %s\n", item.ID, item.LatestObserved, latest.TagName)
		}
	}
	if len(updates) == 0 {
		fmt.Fprintln(output, "direct Go modules have no newer stable versions")
	}
	return nil
}

func goModuleUpdates(root string) ([][3]string, error) {
	command := exec.Command("go", "list", "-m", "-u", "-f", "{{if and (not .Main) (not .Indirect)}}{{.Path}}|{{.Version}}|{{with .Update}}{{.Version}}{{end}}{{end}}", "all")
	command.Dir = root
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("query Go module updates: %w", err)
	}
	var updates [][3]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) == 3 && parts[2] != "" && parts[1] != parts[2] {
			updates = append(updates, [3]string{parts[0], parts[1], parts[2]})
		}
	}
	return updates, nil
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func sortedPairs(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}
