// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"
)

func TestLifecycleOverridesRetainTargetCNIAndSourceAgentIdentity(t *testing.T) {
	values, err := mergeValues(`nodeAgent:
  image: {repository: example, digest: target}
  releaseIdentity: {version: target, manifestDigest: target}
  cniReleaseIdentity: {version: target, manifestDigest: target}
  nodeSelector: {}
controller: {enabled: true}
`, `nodeAgent:
  observationCapabilityHold: true
  transitionPlanID: exact-plan
  releaseIdentity: {version: source, manifestDigest: source}
`)
	if err != nil {
		t.Fatal(err)
	}
	agent := values["nodeAgent"].(map[string]any)
	if agent["observationCapabilityHold"] != true || agent["transitionPlanID"] != "exact-plan" ||
		!reflect.DeepEqual(agent["releaseIdentity"], map[string]any{"version": "source", "manifestDigest": "source"}) ||
		!reflect.DeepEqual(agent["cniReleaseIdentity"], map[string]any{"version": "target", "manifestDigest": "target"}) ||
		!reflect.DeepEqual(agent["image"], map[string]any{"repository": "example", "digest": "target"}) {
		t.Fatalf("unsafe staged values: %v", agent)
	}
}

func TestHelmLibraryStagesAndActivatesWithoutExecutables(t *testing.T) {
	artifact := waycloakctl.Artifact{Repository: "oci://ghcr.io/amoenus/waycloak", Digest: "sha256:" + strings.Repeat("a", 64)}
	configuration := &action.Configuration{
		Releases:     storage.Init(driver.NewMemory()),
		KubeClient:   &kubefake.FailingKubeClient{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}},
		Capabilities: common.DefaultCapabilities,
	}
	runtime := HelmRuntime{Configuration: configuration, Namespace: "waycloak-system", charts: map[waycloakctl.Artifact]*chartv2.Chart{
		artifact: {Metadata: &chartv2.Metadata{APIVersion: "v2", Name: "waycloak", Version: "1.0.2"}},
	}}
	plan := waycloakctl.InstallPlan{Namespace: "waycloak-system", Release: "waycloak", Chart: artifact, Values: "nodeAgent: {enabled: true}\n"}
	// Empty PATH proves that this path does not need a local Helm/CLI binary.
	t.Setenv("PATH", "")
	for index, overrides := range []string{"nodeAgent: {enabled: false}\n", "nodeAgent: {observationCapabilityHold: true}\n", ""} {
		if err := runtime.Apply(context.Background(), plan, overrides); err != nil {
			t.Fatal(err)
		}
		stored, err := configuration.Releases.Last(plan.Release)
		if err != nil {
			t.Fatal(err)
		}
		release := stored.(*releasev1.Release)
		if release.Version != index+1 || release.Info.Status != releasecommon.StatusDeployed || release.ApplyMethod != "ssa" {
			t.Fatalf("unexpected release: revision=%d status=%s apply=%s", release.Version, release.Info.Status, release.ApplyMethod)
		}
		agent := release.Config["nodeAgent"].(map[string]any)
		if index == 0 && agent["enabled"] != false || index == 1 && agent["observationCapabilityHold"] != true || index == 2 && agent["observationCapabilityHold"] != nil {
			t.Fatalf("incorrect stage %d: %v", index, agent)
		}
	}
}

func TestHelmLibraryDoesNotStealPendingOperation(t *testing.T) {
	artifact := waycloakctl.Artifact{Repository: "oci://ghcr.io/amoenus/waycloak", Digest: "sha256:" + strings.Repeat("a", 64)}
	configuration := &action.Configuration{Releases: storage.Init(driver.NewMemory()), KubeClient: &kubefake.FailingKubeClient{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}}, Capabilities: common.DefaultCapabilities}
	runtime := HelmRuntime{Configuration: configuration, Namespace: "waycloak-system", charts: map[waycloakctl.Artifact]*chartv2.Chart{artifact: {Metadata: &chartv2.Metadata{APIVersion: "v2", Name: "waycloak", Version: "1.0.2"}}}}
	plan := waycloakctl.InstallPlan{Namespace: "waycloak-system", Release: "waycloak", Chart: artifact, Values: "nodeAgent: {enabled: true}\n"}
	if err := runtime.Apply(context.Background(), plan, ""); err != nil {
		t.Fatal(err)
	}
	stored, err := configuration.Releases.Last(plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	release := stored.(*releasev1.Release)
	release.Info.Status = releasecommon.StatusPendingUpgrade
	if err := configuration.Releases.Update(release); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Apply(context.Background(), plan, ""); err == nil {
		t.Fatal("pending operation was silently taken over")
	}
	storedAfter, err := configuration.Releases.Last(plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	after := storedAfter.(*releasev1.Release)
	if after.Version != release.Version || after.Info.Status != releasecommon.StatusPendingUpgrade {
		t.Fatal("foreign pending operation changed")
	}
}

func TestHelmRefusesUnboundArtifactsBeforeNetwork(t *testing.T) {
	r := HelmRuntime{}
	for _, chart := range []waycloakctl.Artifact{
		{Repository: "oci://untrusted.invalid/chart", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Repository: "oci://ghcr.io/amoenus/waycloak", Digest: "latest"},
	} {
		if _, err := r.CRDIdentities(context.Background(), chart); err == nil {
			t.Fatal("unbound chart accepted")
		}
	}
	if err := r.Apply(context.Background(), waycloakctl.InstallPlan{}, ""); err == nil {
		t.Fatal("unbound installation accepted")
	}
}

func TestJournalOwnedHelmInterruptionRecovery(t *testing.T) {
	for _, status := range []releasecommon.Status{releasecommon.StatusPendingInstall, releasecommon.StatusPendingUpgrade} {
		for _, foreign := range []string{"", "uid", "plan", "phase"} {
			t.Run(string(status)+"/"+foreign, func(t *testing.T) {
				artifact := waycloakctl.Artifact{Repository: "oci://ghcr.io/amoenus/waycloak", Digest: "sha256:" + strings.Repeat("a", 64)}
				configuration := &action.Configuration{Releases: storage.Init(driver.NewMemory()), KubeClient: &kubefake.FailingKubeClient{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}}, Capabilities: common.DefaultCapabilities}
				runtime := HelmRuntime{Configuration: configuration, Namespace: "waycloak-system", Operation: &Operation{InstallationUID: "installation-one", PlanID: "sha256:" + strings.Repeat("b", 64), Phase: waycloakctl.NativePhaseStage}, charts: map[waycloakctl.Artifact]*chartv2.Chart{artifact: {Metadata: &chartv2.Metadata{APIVersion: "v2", Name: "waycloak", Version: "1.0.2"}}}}
				plan := waycloakctl.InstallPlan{Namespace: "waycloak-system", Release: "waycloak", Chart: artifact, Values: "nodeAgent: {enabled: true}\n"}
				t.Setenv("PATH", "")
				if err := runtime.Apply(context.Background(), plan, "nodeAgent: {observationCapabilityHold: true}\n"); err != nil {
					t.Fatal(err)
				}
				stored, err := configuration.Releases.Last(plan.Release)
				if err != nil {
					t.Fatal(err)
				}
				pending := stored.(*releasev1.Release)
				pending.Info.Status = status
				if foreign != "" {
					pending.Labels["installation.waycloak.io/"+foreign] = "foreign"
				}
				if err := configuration.Releases.Update(pending); err != nil {
					t.Fatal(err)
				}
				err = runtime.Apply(context.Background(), plan, "nodeAgent: {observationCapabilityHold: true}\n")
				last, readErr := configuration.Releases.Last(plan.Release)
				if readErr != nil {
					t.Fatal(readErr)
				}
				after := last.(*releasev1.Release)
				if foreign != "" {
					if err == nil || after.Version != 1 || after.Info.Status != status {
						t.Fatalf("foreign pending operation changed: error=%v revision=%d status=%s", err, after.Version, after.Info.Status)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if after.Version != 2 || after.Info.Status != releasecommon.StatusDeployed || after.Config["nodeAgent"].(map[string]any)["observationCapabilityHold"] != true {
					t.Fatal("recovery did not replay the held phase")
				}
			})
		}
	}
}
