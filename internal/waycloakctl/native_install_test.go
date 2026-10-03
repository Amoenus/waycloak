// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	installv1 "github.com/Amoenus/waycloak/api/installation/v1alpha1"
	"github.com/Amoenus/waycloak/internal/scheduling"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func TestNativeReadinessRequiresFreshNodeCapabilities(t *testing.T) {
	now := time.Now()
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{scheduling.CNIReadyLabel: "true", scheduling.CapabilityEpochLabel: strconv.FormatInt(now.Unix(), 10)}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	if !nativeNodesReady([]corev1.Node{node}, now) {
		t.Fatal("fresh ready capability rejected")
	}
	if nativeNodesReady([]corev1.Node{node}, now.Add(time.Minute)) {
		t.Fatal("stale node capability accepted")
	}
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	if nativeNodesReady([]corev1.Node{node}, now) {
		t.Fatal("unready node accepted")
	}
	node.Status.Conditions[0].Status = corev1.ConditionTrue
	delete(node.Labels, scheduling.CNIReadyLabel)
	if nativeNodesReady([]corev1.Node{node}, now) || nativeNodesReady(nil, now) {
		t.Fatal("missing node coverage accepted")
	}
}

type nativeTestRuntime struct {
	crds    map[string]string
	applied int
}

func TestNativeLegacyFlannelAdoptionBindsOwnedMigration(t *testing.T) {
	ctx := context.Background()
	clients := supportedClients(t)
	source := releaseManifest()
	objects, identities, _ := testInstallCRDBundle(t)
	if _, _, err := ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	seedInstalledRelease(t, clients, source, "waycloak-system", "waycloak", 1, objects)
	target := source
	target.Version = "v1.0.2-rc.4"
	target.ManifestDigest, _ = target.IdentityDigest()
	snapshot, err := PrepareNativeInstall(ctx, clients, &nativeTestRuntime{crds: identities}, installv1.WaycloakInstallationSpec{Version: target.Version, Namespace: "waycloak-system", Release: "waycloak", OverlayCIDR: "100.96.0.0/16", AdoptExisting: true}, target)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Layout.SourceConfigPath != snapshot.Plan.Source.NodeLayout.ConfigPath || !strings.HasSuffix(snapshot.Layout.ConfigPath, "/05-waycloak.conflist") || !strings.Contains(snapshot.Plan.Values, "migrateLegacySource: true") {
		t.Fatal("legacy adoption did not bind separately owned migration")
	}
	if !reflect.DeepEqual(snapshot.Layout.AgentNodeSelector, snapshot.Plan.Source.NodeLayout.AgentNodeSelector) || !reflect.DeepEqual(snapshot.Layout.InstallerNodeSelector, snapshot.Plan.Source.NodeLayout.InstallerNodeSelector) {
		t.Fatal("migration changed node coverage")
	}
}

func (r *nativeTestRuntime) CRDIdentities(context.Context, Artifact) (map[string]string, error) {
	return r.crds, nil
}
func (r *nativeTestRuntime) Apply(context.Context, InstallPlan, string) error {
	r.applied++
	return nil
}

func TestNativeFreshInstallationOwnsSeparateCNIAndResumesPartialCRDs(t *testing.T) {
	ctx := context.Background()
	clients := supportedClients(t)
	manifest := releaseManifest()
	objects, identities, _ := testInstallCRDBundle(t)
	runtime := &nativeTestRuntime{crds: identities}
	spec := installv1.WaycloakInstallationSpec{Version: manifest.Version, Namespace: "waycloak-system", Release: "waycloak", OverlayCIDR: "100.96.0.0/16"}
	snapshot, err := PrepareNativeInstall(ctx, clients, runtime, spec, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(snapshot.Layout.ConfigPath, "/05-waycloak.conflist") || !strings.HasSuffix(snapshot.Layout.SourceConfigPath, "/10-flannel.conflist") || len(snapshot.Layout.AgentNodeSelector) != 0 {
		t.Fatalf("unexpected layout: %+v", snapshot.Layout)
	}
	if _, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, objects[0], metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeInstall(ctx, clients, runtime, snapshot); err != nil {
		t.Fatalf("partial initial CRD installation cannot resume: %v", err)
	}
	changed := objects[0].DeepCopy()
	changed.Spec.Versions[0].Name = "v1beta2"
	if _, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, changed, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := AdvanceNativeInstall(ctx, clients, runtime, snapshot, NativePhaseStage); err == nil || runtime.applied != 0 {
		t.Fatal("incompatible storage API reached Helm mutation")
	}
}

func TestNativeConfigurationTransitionPreservesTrustAndWithdrawsClassUnderHold(t *testing.T) {
	ctx := context.Background()
	clients := supportedClients(t)
	manifest := releaseManifest()
	objects, identities, _ := testInstallCRDBundle(t)
	if _, _, err := ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	seedInstalledRelease(t, clients, manifest, "waycloak-system", "waycloak", 1, objects)
	runtime := &nativeTestRuntime{crds: identities}
	spec := installv1.WaycloakInstallationSpec{Version: manifest.Version, Namespace: "waycloak-system", Release: "waycloak", OverlayCIDR: "100.96.0.0/16", AdoptExisting: true, Config: installv1.InstallationConfig{ControllerResources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("96Mi")}}}}
	snapshot, err := PrepareNativeInstall(ctx, clients, runtime, spec, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&snapshot.Layout, snapshot.Plan.Source.NodeLayout) || snapshot.CAUID != snapshot.Plan.Source.ObservationCAUID {
		t.Fatal("configuration transition changed CNI or trust")
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(snapshot.Plan.Values), &values); err != nil {
		t.Fatal(err)
	}
	if values["controller"].(map[string]any)["resources"].(map[string]any)["requests"].(map[string]any)["memory"] != "96Mi" {
		t.Fatal("configuration not represented in immutable plan")
	}
	next, err := AdvanceNativeInstall(ctx, clients, runtime, snapshot, NativePhaseHold)
	if err != nil || next != NativePhaseClass {
		t.Fatalf("hold: %s %v", next, err)
	}
	agent, err := clients.Kubernetes.AppsV1().DaemonSets(spec.Namespace).Get(ctx, "waycloak-node-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	held, err := observationCapabilityHeld(agent.Spec.Template.Spec.Containers[0].Args)
	if err != nil || !held {
		t.Fatal("class withdrawal preceded node hold")
	}
	if _, err := AdvanceNativeInstall(ctx, clients, runtime, snapshot, NativePhaseClass); err != nil {
		t.Fatal(err)
	}
	next, err = AdvanceNativeInstall(ctx, clients, runtime, snapshot, NativePhaseClass)
	if err != nil || next != NativePhaseStage || runtime.applied != 0 {
		t.Fatalf("withdrawal did not observe absence before staging: %s %v", next, err)
	}
}
