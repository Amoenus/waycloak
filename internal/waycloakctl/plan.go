// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Amoenus/waycloak/internal/observationrelay"
)

type Artifact struct {
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
}

type ReleaseManifest struct {
	APIVersion     string              `json:"apiVersion"`
	Version        string              `json:"version"`
	ManifestDigest string              `json:"manifestDigest"`
	Chart          Artifact            `json:"chart"`
	Images         map[string]Artifact `json:"images"`
	Profiles       []string            `json:"profiles"`
}

type InstallPlan struct {
	APIVersion      string            `json:"apiVersion"`
	Kind            string            `json:"kind"`
	PlanID          string            `json:"planID"`
	InstallSequence string            `json:"installSequence"`
	Namespace       string            `json:"namespace"`
	Release         string            `json:"release"`
	Manifest        string            `json:"manifestDigest"`
	Chart           Artifact          `json:"chart"`
	Values          string            `json:"valuesYAML"`
	Commands        []string          `json:"commands"`
	Security        []string          `json:"securityChanges"`
	CNIChanges      []string          `json:"cniChanges"`
	Rollback        []string          `json:"rollback"`
	Purge           []string          `json:"purge"`
	SecretObjects   []string          `json:"secretObjects"`
	Metadata        map[string]string `json:"metadata"`
}

const controllerFirstInstallSequence = "ControllerFirstCoreActivation-v1"

func LoadReleaseManifest(path string) (ReleaseManifest, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ReleaseManifest{}, "", err
	}
	if len(data) > 1<<20 {
		return ReleaseManifest{}, "", errors.New("release manifest exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest ReleaseManifest
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, "", fmt.Errorf("decode release manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return manifest, "", errors.New("release manifest contains trailing JSON")
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := manifest.Validate(); err != nil {
		return manifest, digest, err
	}
	return manifest, digest, nil
}

func (manifest ReleaseManifest) Validate() error {
	if manifest.APIVersion != "release.waycloak.io/v1" || manifest.Version == "" || !validDigest(manifest.ManifestDigest) {
		return errors.New("release manifest identity is invalid")
	}
	profiles := append([]string(nil), manifest.Profiles...)
	sort.Strings(profiles)
	for index, profile := range profiles {
		if profile == "" || index > 0 && profile == profiles[index-1] {
			return errors.New("release manifest profiles must be non-empty and unique")
		}
	}
	if !contains(profiles, "networking.waycloak.io/Core-v1") {
		return errors.New("release manifest does not attest the Core profile")
	}
	requiredImages := []string{"replacement-controller", "waycloak-cni", "waycloak-node-agent", "waycloak-gateway-agent", "gluetun", "pause"}
	if len(manifest.Images) != len(requiredImages) {
		return errors.New("release manifest image inventory must contain only the required artifacts")
	}
	for _, name := range requiredImages {
		artifact, ok := manifest.Images[name]
		if !ok || artifact.Repository == "" || !validDigest(artifact.Digest) || strings.Contains(artifact.Repository, "@") {
			return fmt.Errorf("release manifest lacks exact %s image identity", name)
		}
	}
	if manifest.Chart.Repository == "" || !validDigest(manifest.Chart.Digest) || strings.Contains(manifest.Chart.Repository, "@") {
		return errors.New("release manifest lacks exact chart identity")
	}
	digest, err := manifest.IdentityDigest()
	if err != nil {
		return fmt.Errorf("compute release manifest identity: %w", err)
	}
	if manifest.ManifestDigest != digest {
		return fmt.Errorf("release manifest digest does not match canonical identity: want %s", digest)
	}
	return nil
}

// IdentityDigest returns the deterministic digest of every release identity
// field except ManifestDigest itself. Profiles are canonicalized as a set and
// encoding/json deterministically orders the image map keys.
func (manifest ReleaseManifest) IdentityDigest() (string, error) {
	profiles := append([]string(nil), manifest.Profiles...)
	sort.Strings(profiles)
	payload := struct {
		APIVersion string              `json:"apiVersion"`
		Version    string              `json:"version"`
		Chart      Artifact            `json:"chart"`
		Images     map[string]Artifact `json:"images"`
		Profiles   []string            `json:"profiles"`
	}{APIVersion: manifest.APIVersion, Version: manifest.Version, Chart: manifest.Chart, Images: manifest.Images, Profiles: profiles}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func BuildInstallPlan(manifest ReleaseManifest, namespace, release string, report PreflightReport) (InstallPlan, error) {
	if err := manifest.Validate(); err != nil {
		return InstallPlan{}, err
	}
	if !report.Compatible || report.CNI.ConfigPath == "" || namespace == "" || release == "" {
		return InstallPlan{}, errors.New("a compatible preflight report and explicit namespace/release are required")
	}
	controller := manifest.Images["replacement-controller"]
	cni := manifest.Images["waycloak-cni"]
	agent := manifest.Images["waycloak-node-agent"]
	pause := manifest.Images["pause"]
	gatewayAgent := manifest.Images["waycloak-gateway-agent"]
	engine := manifest.Images["gluetun"]
	controllerService := chartFullname(release) + "-controller"
	values := fmt.Sprintf(`releaseIdentity:
  version: %q
  manifestDigest: %q
controller:
  enabled: true
  image:
    repository: %q
    digest: %q
  observationTLSSecret: %q
  gateway:
    engineImage:
      repository: %q
      digest: %q
    agentImage:
      repository: %q
      digest: %q
    overlayCIDR: %q
cniInstaller:
  enabled: true
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
  image:
    repository: %q
    digest: %q
  observationRelayURL: %q
  observationCASecret: %q
  cniReceiptHostPath: %q
  cniBinaryHostPath: %q
  cniConfigHostPath: %q
  releaseIdentity:
    version: %q
    manifestDigest: %q
defaultGatewayClass:
  enabled: true
  releaseIdentity:
    version: %q
    manifestDigest: %q
`, manifest.Version, manifest.ManifestDigest, controller.Repository, controller.Digest, release+"-observation-tls", engine.Repository, engine.Digest, gatewayAgent.Repository, gatewayAgent.Digest, report.Networking.OverlayCIDR, cni.Repository, cni.Digest, pause.Repository, pause.Digest,
		report.CNI.ConfigPath, report.CNI.BinaryPath, agent.Repository, agent.Digest,
		"https://"+controllerService+"."+namespace+".svc:9443"+observationrelay.ReportPath, release+"-observation-ca",
		"/var/lib/cni/waycloak/install-receipt.json", report.CNI.BinaryPath, report.CNI.ConfigPath,
		manifest.Version, manifest.ManifestDigest, manifest.Version, manifest.ManifestDigest)
	planID := digestBytes([]byte(manifest.ManifestDigest + "\x00" + namespace + "\x00" + release + "\x00" + controllerFirstInstallSequence + "\x00" + values))
	return InstallPlan{
		APIVersion: OutputAPIVersion, Kind: "InstallPlan", PlanID: planID, InstallSequence: controllerFirstInstallSequence, Namespace: namespace, Release: release, Manifest: manifest.ManifestDigest, Chart: manifest.Chart, Values: values,
		Commands: []string{
			"waycloakctl install apply --plan <reviewed-plan.json> --confirm " + planID,
			"on a clean cluster: helm upgrade --install " + release + " " + manifest.Chart.Repository + "@" + manifest.Chart.Digest + " --namespace " + namespace + " --values <reviewed-values.yaml> --values <controller-first-bootstrap.yaml> --wait",
			"helm upgrade --install " + release + " " + manifest.Chart.Repository + "@" + manifest.Chart.Digest + " --namespace " + namespace + " --values <reviewed-values.yaml> --wait",
		},
		Security:      []string{"create a Pod Security privileged namespace for release-owned node components", "install a privileged root node-agent DaemonSet", "mount exact CNI/netns/state host paths", "install cluster-scoped CRDs, admission policies, and least-privilege RBAC"},
		CNIChanges:    []string{"atomically append waycloak-cni after the primary plugin in " + report.CNI.ConfigPath, "install the exact CNI binary at " + report.CNI.BinaryPath, "preserve the original chain and write a release-bound receipt"},
		Rollback:      []string{"stop newly enrolled workloads while the deny path remains", "helm rollback " + release + " <reviewed-revision>", "verify node receipts and protected packet denial before restarting workloads"},
		Purge:         []string{"normal Helm uninstall does not delete CRDs or restore the CNI chain", "destructive CRD purge and CNI restoration are separate confirmation-gated operations"},
		SecretObjects: []string{release + "-observation-ca", release + "-observation-tls"},
		Metadata:      map[string]string{"cni": report.CNI.Name, "profile": report.Profile},
	}, nil
}

func chartFullname(release string) string {
	if strings.Contains(release, "waycloak") {
		return release
	}
	return release + "-waycloak"
}

func EncodePlan(plan InstallPlan) ([]byte, error) {
	data, err := json.MarshalIndent(plan, "", "  ")
	return append(data, '\n'), err
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func contains(values []string, wanted string) bool {
	values = append([]string(nil), values...)
	sort.Strings(values)
	index := sort.SearchStrings(values, wanted)
	return index < len(values) && values[index] == wanted
}
