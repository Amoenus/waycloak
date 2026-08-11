// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"io"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const OutputAPIVersion = "cli.waycloak.io/v1"

type Clients struct {
	Kubernetes               kubernetes.Interface
	APIExtensions            apiextensionsclient.Interface
	Dynamic                  dynamic.Interface
	Discovery                discovery.DiscoveryInterface
	ClusterServerFingerprint string
	ClusterTrustFingerprint  string
}

type ClientFactory func(context.Context, string, string) (*Clients, error)

type Dependencies struct {
	Stdout     io.Writer
	Stderr     io.Writer
	Clients    ClientFactory
	RunCommand func(context.Context, string, ...string) ([]byte, error)
}

type Check struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	Remediation string `json:"remediation,omitempty"`
}

type PreflightReport struct {
	APIVersion        string          `json:"apiVersion"`
	Kind              string          `json:"kind"`
	ObservationDigest string          `json:"observationDigest"`
	Compatible        bool            `json:"compatible"`
	Profile           string          `json:"profile,omitempty"`
	Identity          ClusterIdentity `json:"identity"`
	Cluster           ClusterSummary  `json:"cluster"`
	CNI               CNISummary      `json:"cni"`
	Networking        NetworkSummary  `json:"networking"`
	Checks            []Check         `json:"checks"`
}

type ClusterIdentity struct {
	ServerFingerprint     string `json:"serverFingerprint"`
	TrustFingerprint      string `json:"trustFingerprint"`
	ClusterUIDFingerprint string `json:"clusterUIDFingerprint"`
}

type ClusterSummary struct {
	KubernetesVersion string         `json:"kubernetesVersion"`
	NodeCount         int            `json:"nodeCount"`
	Architectures     map[string]int `json:"architectures"`
	Runtimes          map[string]int `json:"runtimes"`
	RuntimeVersions   map[string]int `json:"runtimeVersions"`
	Kernels           map[string]int `json:"kernels"`
	OperatingSystems  map[string]int `json:"operatingSystems"`
}

type CNISummary struct {
	Name           string `json:"name,omitempty"`
	ConfigPath     string `json:"configPath,omitempty"`
	BinaryPath     string `json:"binaryPath,omitempty"`
	IdentityDigest string `json:"identityDigest,omitempty"`
}

type NetworkSummary struct {
	PodCIDRs      []string `json:"podCIDRs,omitempty"`
	OverlayCIDR   string   `json:"overlayCIDR"`
	DNSObserved   bool     `json:"dnsObserved"`
	DNSServiceIP  string   `json:"dnsServiceIP,omitempty"`
	ClusterDomain string   `json:"clusterDomain,omitempty"`
}
