// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const testedGluetun = "docker.io/qmcgaw/gluetun:v3.41.0@sha256:6b54856716d0de56e5bb00a77029b0adea57284cf5a466f23aad5979257d3045"

var sha256Reference = regexp.MustCompile(`^.+@sha256:[a-f0-9]{64}$`)

var requiredArtifacts = []string{"controllerImage", "agentImage", "gatewayManagerImage", "qbittorrentAdapterImage", "helmChart", "kclModule"}

type manifest struct {
	SchemaVersion string              `json:"schemaVersion"`
	Version       string              `json:"version"`
	Source        source              `json:"source"`
	Artifacts     map[string]artifact `json:"artifacts"`
	Compatibility compatibility       `json:"compatibility"`
	Security      security            `json:"security"`
}

type source struct {
	Repository  string `json:"repository"`
	Commit      string `json:"commit"`
	WorkflowRun string `json:"workflowRun"`
}

type artifact struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

type compatibility struct {
	Kubernetes                    []string          `json:"kubernetes"`
	CNI                           []string          `json:"cni"`
	CRDStorageVersion             string            `json:"crdStorageVersion"`
	WorkloadAdapterProtocol       string            `json:"workloadAdapterProtocol"`
	WorkloadAdapterConformanceKit string            `json:"workloadAdapterConformanceKit"`
	ReferenceAdapters             map[string]string `json:"referenceAdapters"`
}

type security struct {
	TestedGluetun        string              `json:"testedGluetun"`
	RequiredCapabilities map[string][]string `json:"requiredCapabilities"`
}

func main() {
	output := flag.String("output", "release-manifest.json", "output path")
	version := flag.String("version", "", "semantic release version")
	repository := flag.String("repository", "", "source repository URL")
	commit := flag.String("commit", "", "40-character Git commit")
	workflowRun := flag.String("workflow-run", "", "release workflow run URL")
	controller := flag.String("controller", "", "controller digest reference")
	agent := flag.String("agent", "", "agent digest reference")
	gatewayManager := flag.String("gateway-manager", "", "gateway-manager digest reference")
	qbittorrentAdapter := flag.String("qbittorrent-adapter", "", "qBitTorrent adapter digest reference")
	chart := flag.String("chart", "", "Helm chart digest reference")
	kclModule := flag.String("kcl-module", "", "KCL module digest reference")
	flag.Parse()
	value, err := buildManifest(*version, *repository, *commit, *workflowRun, map[string]string{"controllerImage": *controller, "agentImage": *agent, "gatewayManagerImage": *gatewayManager, "qbittorrentAdapterImage": *qbittorrentAdapter, "helmChart": *chart, "kclModule": *kclModule})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(*output, encoded, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func buildManifest(version, repository, commit, workflowRun string, references map[string]string) (manifest, error) {
	if version == "" || repository == "" || workflowRun == "" || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit) {
		return manifest{}, errors.New("version, repository, workflow-run, and a lowercase 40-character commit are required")
	}
	artifacts := make(map[string]artifact, len(references))
	for _, name := range requiredArtifacts {
		if _, ok := references[name]; !ok {
			return manifest{}, fmt.Errorf("required artifact %s is missing", name)
		}
	}
	if len(references) != len(requiredArtifacts) {
		return manifest{}, errors.New("release manifest contains an unknown artifact")
	}
	for name, reference := range references {
		if !sha256Reference.MatchString(reference) {
			return manifest{}, fmt.Errorf("%s must be an immutable SHA-256 reference", name)
		}
		artifacts[name] = artifact{Reference: reference, Digest: reference[strings.LastIndex(reference, "@")+1:]}
	}
	return manifest{
		SchemaVersion: "1.2.0",
		Version:       version,
		Source:        source{Repository: repository, Commit: commit, WorkflowRun: workflowRun},
		Artifacts:     artifacts,
		Compatibility: compatibility{Kubernetes: []string{"1.35", "1.36"}, CNI: []string{"kindnet", "flannel"}, CRDStorageVersion: "v1alpha1", WorkloadAdapterProtocol: "networking.waycloak.io/adapter/v1alpha1", WorkloadAdapterConformanceKit: "workload-adapter-kit-v1alpha1.tar.gz", ReferenceAdapters: map[string]string{"qbittorrent": ">=5.2.3 <6.0.0"}},
		Security: security{TestedGluetun: testedGluetun, RequiredCapabilities: map[string][]string{
			"agent":          {"NET_ADMIN"},
			"gatewayManager": {"NET_ADMIN"},
			"vpnEngine":      {"CHOWN", "DAC_OVERRIDE", "FOWNER", "NET_ADMIN", "SETGID", "SETUID"},
		}},
	}, nil
}
