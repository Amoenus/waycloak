// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"sigs.k8s.io/yaml"
)

func TestBuildValuesProducesExactK3sBootstrap(t *testing.T) {
	manifest := testManifest(t)
	values, err := buildValues(manifest, k3sFlannelAMD64Profile, "")
	if err != nil {
		t.Fatalf("build values: %v", err)
	}
	var decoded map[string]any
	if err := yaml.Unmarshal([]byte(values), &decoded); err != nil {
		t.Fatalf("decode generated values: %v", err)
	}
	for _, wanted := range []string{
		"enabled: true",
		"manifestDigest: \"" + manifest.ManifestDigest + "\"",
		"configHostPath: \"/var/lib/rancher/k3s/agent/etc/cni/net.d/10-flannel.conflist\"",
		"serviceIP: \"10.43.0.10\"",
	} {
		if !strings.Contains(values, wanted) {
			t.Fatalf("generated values do not contain %q:\n%s", wanted, values)
		}
	}
	if strings.Contains(values, "overlayCIDR") {
		t.Fatal("release-owned values unexpectedly contain the user-owned overlay CIDR")
	}
}

func TestBuildValuesRejectsUncertifiedProfile(t *testing.T) {
	manifest := testManifest(t)
	manifest.SupportMatrix.Rows[0].Distribution = "kind"
	if _, err := buildValues(manifest, k3sFlannelAMD64Profile, ""); err == nil {
		t.Fatal("uncertified profile unexpectedly produced bootstrap values")
	}
}

func TestGitOpsManifestsUseStandardPinnedHelmSources(t *testing.T) {
	manifest := testManifest(t)
	values, err := buildValues(manifest, k3sFlannelAMD64Profile, "100.96.0.0/16")
	if err != nil {
		t.Fatalf("build values: %v", err)
	}
	flux := fluxManifest(manifest, values)
	argo := argoCDManifest(manifest, values)
	for _, test := range []struct {
		name   string
		value  string
		wanted []string
	}{
		{name: "flux", value: flux, wanted: []string{"kind: OCIRepository", "kind: HelmRelease", "digest: " + manifest.Chart.Digest, "overlayCIDR: \"100.96.0.0/16\""}},
		{name: "argocd", value: argo, wanted: []string{"kind: Application", "repoURL: registry.example.invalid/charts", "targetRevision: 1.2.3", "valuesObject:", "overlayCIDR: \"100.96.0.0/16\""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var documents []any
			for _, document := range strings.Split(test.value, "\n---\n") {
				var decoded any
				if err := yaml.Unmarshal([]byte(document), &decoded); err != nil {
					t.Fatalf("decode manifest: %v\n%s", err, test.value)
				}
				documents = append(documents, decoded)
			}
			if len(documents) == 0 {
				t.Fatal("no YAML documents decoded")
			}
			for _, wanted := range test.wanted {
				if !strings.Contains(test.value, wanted) {
					t.Fatalf("manifest does not contain %q:\n%s", wanted, test.value)
				}
			}
		})
	}
}

func testManifest(t *testing.T) waycloakctl.ReleaseManifest {
	t.Helper()
	digest := func(character string) string { return "sha256:" + strings.Repeat(character, 64) }
	images := map[string]waycloakctl.Artifact{}
	for index, name := range []string{"replacement-controller", "waycloak-cni", "waycloak-node-agent", "waycloak-gateway-agent", "waycloak-gateway-runtime", "waycloak-qbittorrent-adapter", "gluetun", "coredns", "pause"} {
		images[name] = waycloakctl.Artifact{Repository: "registry.example.invalid/" + name, Digest: digest(string(rune('0' + index)))}
	}
	manifest := waycloakctl.ReleaseManifest{
		APIVersion: "release.waycloak.io/v1",
		Version:    "v1.2.3",
		Chart:      waycloakctl.Artifact{Repository: "oci://registry.example.invalid/charts/waycloak", Digest: digest("c")},
		KCL:        &waycloakctl.Artifact{Repository: "oci://registry.example.invalid/waycloak-kcl", Digest: digest("d")},
		Images:     images,
		Profiles:   []string{"networking.waycloak.io/Core-v1"},
		SupportMatrix: &waycloakctl.ReleaseSupportMatrix{Rows: []waycloakctl.ReleaseSupportRow{{
			ID: "k3s", Kubernetes: "v1.36.1+k3s1", Distribution: "k3s", CNI: "flannel", Runtime: "containerd", Kernel: "linux>=5.10", Architecture: "amd64", Engine: "gluetun", ProviderConfiguration: "protonvpn/openvpn", Features: []string{"networking.waycloak.io/TCP"}, EvidenceSuites: []string{"test"},
		}}},
	}
	identity, err := manifest.IdentityDigest()
	if err != nil {
		t.Fatalf("identity digest: %v", err)
	}
	manifest.ManifestDigest = identity
	return manifest
}
