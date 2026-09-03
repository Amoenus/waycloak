// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Command gitopsvalues emits the release-owned half of the supported
// GitOps bootstrap values. Users keep the small cluster-owned overlay in Git.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path"
	"strings"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
)

const k3sFlannelAMD64Profile = "k3s-flannel-amd64"

type profile struct {
	architecture  string
	cniConfigPath string
	cniBinaryPath string
	dnsServiceIP  string
	clusterDomain string
}

func main() {
	var manifestPath, profileName, format, overlayCIDR string
	flag.StringVar(&manifestPath, "release-manifest", "", "verified Waycloak release manifest")
	flag.StringVar(&profileName, "profile", k3sFlannelAMD64Profile, "certified cluster profile")
	flag.StringVar(&format, "format", "values", "output format: values, flux, or argocd")
	flag.StringVar(&overlayCIDR, "overlay-cidr", "", "reviewed private overlay CIDR; required for Flux and Argo CD")
	flag.Parse()
	if manifestPath == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--release-manifest is required")
		os.Exit(2)
	}
	manifest, _, err := waycloakctl.LoadReleaseManifest(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load release manifest: %v\n", err)
		os.Exit(1)
	}
	values, err := buildValues(manifest, profileName, overlayCIDR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build GitOps values: %v\n", err)
		os.Exit(1)
	}
	switch format {
	case "values":
		if overlayCIDR != "" {
			fmt.Fprintln(os.Stderr, "--overlay-cidr is not accepted with values output; keep cluster choices in a separate file")
			os.Exit(2)
		}
		fmt.Print(values)
	case "flux":
		if err := validateOverlayCIDR(overlayCIDR); err != nil {
			fmt.Fprintf(os.Stderr, "invalid overlay CIDR: %v\n", err)
			os.Exit(2)
		}
		fmt.Print(fluxManifest(manifest, values))
	case "argocd":
		if err := validateOverlayCIDR(overlayCIDR); err != nil {
			fmt.Fprintf(os.Stderr, "invalid overlay CIDR: %v\n", err)
			os.Exit(2)
		}
		fmt.Print(argoCDManifest(manifest, values))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output format %q\n", format)
		os.Exit(2)
	}
}

func buildValues(manifest waycloakctl.ReleaseManifest, profileName, overlayCIDR string) (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	selected, err := certifiedProfile(manifest, profileName)
	if err != nil {
		return "", err
	}
	image := func(name string) waycloakctl.Artifact { return manifest.Images[name] }
	controller := image("replacement-controller")
	cni := image("waycloak-cni")
	agent := image("waycloak-node-agent")
	pause := image("pause")
	engine := image("gluetun")
	gatewayAgent := image("waycloak-gateway-agent")
	coreDNS := image("coredns")
	overlay := ""
	if overlayCIDR != "" {
		if err := validateOverlayCIDR(overlayCIDR); err != nil {
			return "", err
		}
		overlay = fmt.Sprintf("    overlayCIDR: %q\n", overlayCIDR)
	}

	return fmt.Sprintf(`# Generated from a signed Waycloak release manifest. Keep this file
# unchanged and provide cluster-owned choices in a separate values file.
bootstrap:
  observationCertificates:
    enabled: true
releaseIdentity:
  version: %q
  manifestDigest: %q
controller:
  enabled: true
  image:
    repository: %q
    digest: %q
  gateway:
    engineImage:
      repository: %q
      digest: %q
    agentImage:
      repository: %q
      digest: %q
    coreDNSImage:
      repository: %q
      digest: %q
%s
    clusterDNS:
      serviceIP: %q
      domain: %q
cniInstaller:
  enabled: true
  nodeSelector:
    kubernetes.io/arch: %q
  image:
    repository: %q
    digest: %q
  pauseImage:
    repository: %q
    digest: %q
  configHostPath: %q
  binaryHostPath: %q
nodeAgent:
  enabled: true
  nodeSelector:
    kubernetes.io/arch: %q
  image:
    repository: %q
    digest: %q
  cniReceiptHostPath: "/var/lib/cni/waycloak/install-receipt.json"
  cniBinaryHostPath: %q
  cniConfigHostPath: %q
defaultGatewayClass:
  enabled: true
  releaseIdentity:
    version: %q
    manifestDigest: %q
`, manifest.Version, manifest.ManifestDigest,
		controller.Repository, controller.Digest,
		engine.Repository, engine.Digest,
		gatewayAgent.Repository, gatewayAgent.Digest,
		coreDNS.Repository, coreDNS.Digest, strings.TrimSuffix(overlay, "\n"),
		selected.dnsServiceIP, selected.clusterDomain,
		selected.architecture, cni.Repository, cni.Digest,
		pause.Repository, pause.Digest,
		selected.cniConfigPath, selected.cniBinaryPath,
		selected.architecture, agent.Repository, agent.Digest,
		selected.cniBinaryPath, selected.cniConfigPath,
		manifest.Version, manifest.ManifestDigest), nil
}

func validateOverlayCIDR(value string) error {
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 29 || prefix != prefix.Masked() {
		return errors.New("use a canonical private or shared IPv4 prefix from /16 through /29")
	}
	allowed := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	for _, network := range allowed {
		if prefix.Bits() >= network.Bits() && network.Contains(prefix.Addr()) {
			return nil
		}
	}
	return errors.New("use RFC 1918 or RFC 6598 address space")
}

func fluxManifest(manifest waycloakctl.ReleaseManifest, values string) string {
	return fmt.Sprintf(`# Review overlayCIDR for conflicts, commit this file, and reconcile it with Flux.
apiVersion: v1
kind: Namespace
metadata:
  name: waycloak-system
  labels:
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/audit: privileged
    pod-security.kubernetes.io/warn: privileged
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: waycloak
  namespace: waycloak-system
spec:
  interval: 1h
  url: %s
  layerSelector:
    mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
    operation: copy
  ref:
    digest: %s
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: waycloak
  namespace: waycloak-system
spec:
  interval: 30m
  releaseName: waycloak
  chartRef:
    kind: OCIRepository
    name: waycloak
  install:
    crds: Create
  upgrade:
    crds: CreateReplace
  values:
%s`, manifest.Chart.Repository, manifest.Chart.Digest, indent(values, 4))
}

func argoCDManifest(manifest waycloakctl.ReleaseManifest, values string) string {
	repository := strings.TrimPrefix(manifest.Chart.Repository, "oci://")
	chart := path.Base(repository)
	repository = strings.TrimSuffix(repository, "/"+chart)
	return fmt.Sprintf(`# Review overlayCIDR, commit this file, and apply it to the Argo CD namespace.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: waycloak
  namespace: argocd
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: waycloak-system
  source:
    repoURL: %s
    chart: %s
    targetRevision: %s
    helm:
      releaseName: waycloak
      valuesObject:
%s  syncPolicy:
    automated:
      prune: false
      selfHeal: true
    managedNamespaceMetadata:
      labels:
        pod-security.kubernetes.io/enforce: privileged
        pod-security.kubernetes.io/audit: privileged
        pod-security.kubernetes.io/warn: privileged
    syncOptions:
      - CreateNamespace=true
`, repository, chart, strings.TrimPrefix(manifest.Version, "v"), indent(values, 8))
}

func indent(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	for index := range lines {
		lines[index] = prefix + lines[index]
	}
	return strings.Join(lines, "\n") + "\n"
}

func certifiedProfile(manifest waycloakctl.ReleaseManifest, name string) (profile, error) {
	if name != k3sFlannelAMD64Profile {
		return profile{}, fmt.Errorf("unsupported GitOps profile %q", name)
	}
	if manifest.SupportMatrix == nil {
		return profile{}, errors.New("release manifest has no certified support matrix")
	}
	for _, row := range manifest.SupportMatrix.Rows {
		if strings.EqualFold(row.Distribution, "k3s") && strings.EqualFold(row.CNI, "flannel") && row.Architecture == "amd64" {
			return profile{
				architecture:  "amd64",
				cniConfigPath: "/var/lib/rancher/k3s/agent/etc/cni/net.d/10-flannel.conflist",
				cniBinaryPath: "/var/lib/rancher/k3s/data/cni/waycloak-cni",
				dnsServiceIP:  "10.43.0.10",
				clusterDomain: "cluster.local",
			}, nil
		}
	}
	return profile{}, errors.New("release manifest does not certify k3s/flannel on amd64")
}
