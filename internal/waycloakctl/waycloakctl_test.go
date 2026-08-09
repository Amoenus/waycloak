// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestPreflightSupportsPinnedK3sAndRejectsServedAlpha(t *testing.T) {
	clients := supportedClients(t)
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || !report.Compatible || report.CNI.Name != "k3s-flannel" {
		t.Fatalf("supported preflight failed: %#v %v", report, err)
	}
	alpha := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "vpngateways.networking.waycloak.io"}, Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: "networking.waycloak.io", Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "vpngateways", Kind: "VPNGateway"}, Scope: apiextensionsv1.NamespaceScoped, Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true}}}}
	clients.APIExtensions = apiextensionsfake.NewSimpleClientset(alpha)
	report, err = Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || report.Compatible || checkStatus(report, "clean-api") != "Fail" {
		t.Fatalf("served alpha API was not rejected: %#v %v", report, err)
	}
}

func TestPreflightRejectsOverlayOverlapAndUnsupportedNode(t *testing.T) {
	clients := supportedClients(t)
	node, _ := clients.Kubernetes.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	node.Status.NodeInfo.Architecture = "s390x"
	_, _ = clients.Kubernetes.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
	report, err := Preflight(context.Background(), clients, "10.42.0.0/16")
	if err != nil || report.Compatible || checkStatus(report, "nodes") != "Fail" || checkStatus(report, "overlay") != "Fail" {
		t.Fatalf("unsafe node/network passed: %#v %v", report, err)
	}
}

func TestPreflightObservationBindsClusterIdentityAndNodeFacts(t *testing.T) {
	clients := supportedClients(t)
	first, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || !validDigest(first.ObservationDigest) || !validDigest(first.Identity.ClusterUIDFingerprint) {
		t.Fatalf("preflight lacks canonical cluster identity: %#v %v", first, err)
	}
	second, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || second.ObservationDigest != first.ObservationDigest {
		t.Fatalf("unchanged preflight identity drifted: %s != %s: %v", first.ObservationDigest, second.ObservationDigest, err)
	}
	node, err := clients.Kubernetes.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.NodeInfo.KernelVersion = "6.9.0"
	if _, err = clients.Kubernetes.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	changed, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || changed.ObservationDigest == first.ObservationDigest {
		t.Fatalf("node fact change retained preflight identity: %#v %v", changed, err)
	}
}

func TestInstallPlanRequiresExplicitArchitectureOnMixedCluster(t *testing.T) {
	clients := supportedClients(t)
	arm := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "arm"}, Spec: corev1.NodeSpec{PodCIDRs: []string{"10.42.1.0/24"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "arm64", OperatingSystem: "linux", KernelVersion: "6.8.0", ContainerRuntimeVersion: "containerd://2.1.0"}}}
	if _, err := clients.Kubernetes.CoreV1().Nodes().Create(context.Background(), arm, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	source, crds := absentInstallInputs(t)
	if _, err = BuildInstallPlan(releaseManifest(), "waycloak-system", "waycloak", "", report, source, crds); err == nil || !strings.Contains(err.Error(), "explicit --node-architecture") {
		t.Fatalf("mixed cluster did not require an explicit row: %v", err)
	}
	plan, err := BuildInstallPlan(releaseManifest(), "waycloak-system", "waycloak", "amd64", report, source, crds)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NodeArchitecture != "amd64" || strings.Count(plan.Values, "kubernetes.io/arch: \"amd64\"") != 2 {
		t.Fatalf("plan did not constrain both node components: %#v\n%s", plan, plan.Values)
	}
}

func TestInstallPlanHasNoCredentialValuesAndRequiresExactConfirmation(t *testing.T) {
	manifest := releaseManifest()
	report, err := Preflight(context.Background(), supportedClients(t), "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	source, crds := absentInstallInputs(t)
	plan, err := BuildInstallPlan(manifest, "waycloak-system", "waycloak", "", report, source, crds)
	if err != nil {
		t.Fatal(err)
	}
	nestedIdentity := "  releaseIdentity:\n    version: " + strconv.Quote(manifest.Version) + "\n    manifestDigest: " + strconv.Quote(manifest.ManifestDigest) + "\n"
	if count := strings.Count(plan.Values, nestedIdentity); count != 2 {
		t.Fatalf("install values contain %d nested runtime release identities, want node agent and default class", count)
	}
	if !strings.Contains(plan.Values, `observationRelayURL: "https://waycloak-controller.waycloak-system.svc:9443/node-observations/v1/report"`) {
		t.Fatalf("install values do not use the controller observation relay contract: %s", plan.Values)
	}
	if plan.InstallSequence != controllerFirstInstallSequence || len(plan.Commands) != 3 {
		t.Fatalf("install plan does not expose the controller-first sequence: %#v", plan)
	}
	encoded, err := EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"username", "password", "privateKey", "latest"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("plan contains forbidden value %q", forbidden)
		}
	}
	clients := supportedClients(t)
	if err := ApplyInstallPlan(context.Background(), clients, nil, plan, "wrong"); err == nil || !strings.Contains(err.Error(), "refusing mutation") {
		t.Fatalf("wrong confirmation did not fail: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().Namespaces().Get(context.Background(), "waycloak-system", metav1.GetOptions{}); !errorsIsNotFound(err) {
		t.Fatalf("refused apply mutated the cluster: %v", err)
	}
}

func TestReleaseManifestIdentityRejectsTamperingAndExtraArtifacts(t *testing.T) {
	manifest := releaseManifest()
	manifest.Version = "v1.0.0-tampered"
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "canonical identity") {
		t.Fatalf("tampered manifest identity was accepted: %v", err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release-manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadReleaseManifest(path); err == nil || !strings.Contains(err.Error(), "canonical identity") {
		t.Fatalf("loader accepted a tampered manifest identity: %v", err)
	}

	manifest = releaseManifest()
	manifest.Images["unreviewed-backend"] = Artifact{Repository: "example.invalid/unreviewed", Digest: "sha256:" + strings.Repeat("9", 64)}
	digest, err := manifest.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestDigest = digest
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "only the required artifacts") {
		t.Fatalf("extra release artifact was accepted: %v", err)
	}
}

func TestReleaseManifestIdentityIsFormattingAndProfileOrderIndependent(t *testing.T) {
	manifest := releaseManifest()
	manifest.Profiles = []string{"networking.waycloak.io/Extended-v1", "networking.waycloak.io/Core-v1"}
	first, err := manifest.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Profiles[0], manifest.Profiles[1] = manifest.Profiles[1], manifest.Profiles[0]
	second, err := manifest.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("profile ordering changed canonical identity: %s != %s", first, second)
	}
	manifest.ManifestDigest = second
	if err := manifest.Validate(); err != nil {
		t.Fatalf("canonical manifest was rejected: %v", err)
	}
}

func TestInstallApplyCreatesInMemoryTLSAndRejectsTampering(t *testing.T) {
	manifest := releaseManifest()
	clients := supportedClients(t)
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	testCRDs, crds, crdBundle := testInstallCRDBundle(t)
	source, err := ObserveInstalledRelease(context.Background(), clients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildInstallPlan(manifest, "waycloak-system", "waycloak", "", report, source, crds)
	if err != nil {
		t.Fatal(err)
	}
	encodedPlan, err := EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(t.TempDir(), "install-plan.json")
	if err = os.WriteFile(planPath, encodedPlan, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err = LoadInstallPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	upgradeCalls := 0
	activeManifest := manifest
	revision := int64(0)
	runner := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name != "helm" {
			t.Fatalf("unexpected Helm command: %s %#v", name, arguments)
		}
		if len(arguments) >= 2 && arguments[0] == "show" && arguments[1] == "crds" {
			return crdBundle, nil
		}
		if !containsString(arguments, "@"+activeManifest.Chart.Digest) {
			t.Fatalf("unexpected Helm chart: %#v", arguments)
		}
		upgradeCalls++
		values := make([]string, 0, 2)
		for index, argument := range arguments {
			if argument == "--values" && index+1 < len(arguments) {
				data, err := os.ReadFile(arguments[index+1])
				if err != nil {
					t.Fatal(err)
				}
				values = append(values, string(data))
			}
		}
		if upgradeCalls == 1 && (len(values) != 2 || values[1] != controllerFirstBootstrapValues) {
			t.Fatalf("clean install did not use exact controller-first overrides: %#v", values)
		}
		if upgradeCalls > 1 && len(values) != 1 {
			t.Fatalf("Core activation or existing-release apply used bootstrap overrides: %#v", values)
		}
		if upgradeCalls >= 2 {
			revision++
			if revision == 1 {
				revision = 2
			}
			seedInstalledRelease(t, clients, activeManifest, plan.Namespace, plan.Release, revision, testCRDs)
		}
		return nil, nil
	}
	if err := ApplyInstallPlan(context.Background(), clients, runner, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if upgradeCalls != 2 {
		t.Fatalf("Helm upgrade was called %d times", upgradeCalls)
	}
	namespace, err := clients.Kubernetes.CoreV1().Namespaces().Get(context.Background(), plan.Namespace, metav1.GetOptions{})
	if err != nil || namespace.Labels["pod-security.kubernetes.io/enforce"] != "privileged" {
		t.Fatalf("reviewed system namespace missing: %#v %v", namespace, err)
	}
	tlsSecret, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(context.Background(), plan.Release+"-observation-tls", metav1.GetOptions{})
	if err != nil || len(tlsSecret.Data["tls.key"]) == 0 {
		t.Fatalf("in-memory TLS identity missing: %v", err)
	}
	initialTLSUID := tlsSecret.UID
	forward := manifest
	forward.Version = "v1.0.0-beta.2"
	forward.ManifestDigest, err = forward.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	forwardSource, err := ObserveInstalledRelease(context.Background(), clients, plan.Namespace, plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	forwardPlan, err := BuildInstallPlan(forward, plan.Namespace, plan.Release, "", report, forwardSource, crds)
	if err != nil {
		t.Fatal(err)
	}
	activeManifest = forward
	if err := ApplyInstallPlan(context.Background(), clients, runner, forwardPlan, forwardPlan.PlanID); err != nil {
		t.Fatal(err)
	}
	if upgradeCalls != 3 {
		t.Fatalf("exact transition called Helm %d total times, want 3", upgradeCalls)
	}
	tlsSecret, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(context.Background(), plan.Release+"-observation-tls", metav1.GetOptions{})
	if err != nil || tlsSecret.UID != initialTLSUID {
		t.Fatalf("ordinary transition replaced serving identity: %#v %v", tlsSecret, err)
	}
	rollbackSource, err := ObserveInstalledRelease(context.Background(), clients, plan.Namespace, plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	rollbackPlan, err := BuildInstallPlan(manifest, plan.Namespace, plan.Release, "", report, rollbackSource, crds)
	if err != nil {
		t.Fatal(err)
	}
	activeManifest = manifest
	if err := ApplyInstallPlan(context.Background(), clients, runner, rollbackPlan, rollbackPlan.PlanID); err != nil {
		t.Fatal(err)
	}
	if upgradeCalls != 4 {
		t.Fatalf("exact rollback called Helm %d total times, want 4", upgradeCalls)
	}
	tamperSource, err := ObserveInstalledRelease(context.Background(), clients, plan.Namespace, plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	tamperPlan, err := BuildInstallPlan(forward, plan.Namespace, plan.Release, "", report, tamperSource, crds)
	if err != nil {
		t.Fatal(err)
	}
	tlsSecret.Data["tls.crt"] = []byte("tampered")
	if _, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Update(context.Background(), tlsSecret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyInstallPlan(context.Background(), clients, runner, tamperPlan, tamperPlan.PlanID); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("tampered install identity accepted: %v", err)
	}
	if upgradeCalls != 4 {
		t.Fatal("tampered source reached Helm mutation")
	}
}

func TestInstallApplyRejectsPreflightDriftBeforeMutation(t *testing.T) {
	clients := supportedClients(t)
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	source, crds := absentInstallInputs(t)
	plan, err := BuildInstallPlan(releaseManifest(), "waycloak-system", "waycloak", "", report, source, crds)
	if err != nil {
		t.Fatal(err)
	}
	node, err := clients.Kubernetes.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.NodeInfo.KernelVersion = "6.9.0"
	if _, err = clients.Kubernetes.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	called := false
	runner := func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	err = ApplyInstallPlan(context.Background(), clients, runner, plan, plan.PlanID)
	if err == nil || !strings.Contains(err.Error(), "preflight observation changed") || called {
		t.Fatalf("drift did not fail before Helm: called=%t err=%v", called, err)
	}
	if _, err = clients.Kubernetes.CoreV1().Namespaces().Get(context.Background(), plan.Namespace, metav1.GetOptions{}); !errorsIsNotFound(err) {
		t.Fatalf("drift refusal mutated the cluster: %v", err)
	}
}

func TestInstallObservationSecretsRecoverOnlyFromServingIdentity(t *testing.T) {
	clients := supportedClients(t)
	ctx := context.Background()
	if _, err := clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "waycloak-system"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	planID := "sha256:" + strings.Repeat("a", 64)
	ca, serving, err := ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", planID)
	if err != nil {
		t.Fatal(err)
	}
	originalCA := append([]byte(nil), ca.Data["ca.crt"]...)
	originalServingUID := serving.UID
	if err = clients.Kubernetes.CoreV1().Secrets("waycloak-system").Delete(ctx, ca.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	ca, serving, err = ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", planID)
	if err != nil || !bytes.Equal(ca.Data["ca.crt"], originalCA) || serving.UID != originalServingUID {
		t.Fatalf("partial retry did not reconstruct public CA from stable serving identity: %#v %#v %v", ca, serving, err)
	}
	if err = clients.Kubernetes.CoreV1().Secrets("waycloak-system").Delete(ctx, serving.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", planID); err == nil || !strings.Contains(err.Error(), "explicit certificate-rotation") {
		t.Fatalf("missing serving private key was silently regenerated: %v", err)
	}
}

func TestChartCRDIdentityAndStorageChangeGate(t *testing.T) {
	_, identities, bundle := testInstallCRDBundle(t)
	chart := releaseManifest().Chart
	runner := func(context.Context, string, ...string) ([]byte, error) { return bundle, nil }
	observed, err := ChartCRDIdentities(context.Background(), runner, chart)
	if err != nil || !reflect.DeepEqual(observed, identities) {
		t.Fatalf("exact chart CRD inventory was not canonical: %#v %v", observed, err)
	}
	source, err := finalizeInstalledReleaseObservation(InstalledReleaseObservation{State: installStateAbsent, CRDIdentities: identities})
	if err != nil {
		t.Fatal(err)
	}
	changed := copyStringMap(identities)
	changed[stateCRDNames[0]] = "sha256:" + strings.Repeat("f", 64)
	if err = validateInstallCRDTransition(source, changed); err == nil || !strings.Contains(err.Error(), "storage migration") {
		t.Fatalf("unplanned CRD transition was accepted: %v", err)
	}
	extra := append(append([]byte(nil), bundle...), []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"unexpected"}}`)...)
	if _, err = ChartCRDIdentities(context.Background(), func(context.Context, string, ...string) ([]byte, error) { return extra, nil }, chart); err == nil || !strings.Contains(err.Error(), "unexpected CRD") {
		t.Fatalf("unexpected chart document was accepted: %v", err)
	}
}

func TestGatewayRecipeContainsReferencesButNoCredentialValues(t *testing.T) {
	value, err := RenderGatewayRecipe(GatewayRecipe{Namespace: "media", Name: "private", ClassName: "gluetun.waycloak.io", ConfigMapName: "proton", SecretName: "proton-credentials", Provider: "protonvpn", Protocol: "openvpn", OverlayCIDR: "100.96.0.0/16", AllowDisruptiveVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(value, "credentialRefs:") || !strings.Contains(value, "name: proton-credentials") || !strings.Contains(value, "verify.waycloak.io/dedicated") || strings.Contains(strings.ToLower(value), "password:") || strings.Contains(strings.ToLower(value), "username:") {
		t.Fatalf("unsafe recipe:\n%s", value)
	}
}

func TestTunnelLossTargetsOnlyExactOwnedGatewayPod(t *testing.T) {
	controller := true
	gateway := &unstructured.Unstructured{}
	gateway.SetAPIVersion("networking.waycloak.io/v1beta1")
	gateway.SetKind("VPNGateway")
	gateway.SetName("private")
	gateway.SetNamespace("media")
	gateway.SetUID("gateway-uid")
	statefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "media", UID: "statefulset-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: gateway.GetAPIVersion(), Kind: gateway.GetKind(), Name: gateway.GetName(), UID: gateway.GetUID(), Controller: &controller}}}}
	exact := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "exact", Namespace: "media", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: statefulSet.Name, UID: statefulSet.UID, Controller: &controller}}}}
	foreign := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "media", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "other", UID: "other-uid", Controller: &controller}}}}
	clients := supportedClients(t)
	clients.Kubernetes = kubernetesfake.NewSimpleClientset(statefulSet, exact, foreign)
	if err := deleteExactGatewayPod(context.Background(), clients, "media", gateway); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Kubernetes.CoreV1().Pods("media").Get(context.Background(), "exact", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("exact Pod was not deleted: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().Pods("media").Get(context.Background(), "foreign", metav1.GetOptions{}); err != nil {
		t.Fatalf("foreign Pod was touched: %v", err)
	}
}

func TestVerifyConfirmationBindsProbeEndpointAndPublicCA(t *testing.T) {
	base := verifyConfirmation("media", "private", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "observer-ca")
	for name, value := range map[string]string{
		"namespace": verifyConfirmation("other", "private", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "observer-ca"),
		"gateway":   verifyConfirmation("media", "other", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "observer-ca"),
		"image":     verifyConfirmation("media", "private", "registry.invalid/probe@sha256:"+strings.Repeat("b", 64), "https://198.18.0.1:8443/ip", "observer-ca"),
		"url":       verifyConfirmation("media", "private", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.2:8443/ip", "observer-ca"),
		"ca":        verifyConfirmation("media", "private", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "other-ca"),
	} {
		if value == base {
			t.Fatalf("%s was not bound into the disruptive verification identity", name)
		}
	}
}

func TestProbePodUsesHTTPSObserverAndPublicCAWithoutCredentials(t *testing.T) {
	pod := probePod("probe", "media", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "observer-ca", map[string]string{"verify.waycloak.io/run": "test"}, nil)
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || len(pod.Spec.Containers) != 1 {
		t.Fatalf("probe received a Kubernetes credential or unexpected containers: %#v", pod.Spec)
	}
	container := pod.Spec.Containers[0]
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].ConfigMap == nil || pod.Spec.Volumes[0].ConfigMap.Name != "observer-ca" || len(container.VolumeMounts) != 1 || !container.VolumeMounts[0].ReadOnly {
		t.Fatalf("probe public CA is not a read-only ConfigMap mount: %#v %#v", pod.Spec.Volumes, container.VolumeMounts)
	}
	if container.SecurityContext == nil || container.SecurityContext.RunAsNonRoot == nil || !*container.SecurityContext.RunAsNonRoot || len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("probe security context regressed: %#v", container.SecurityContext)
	}
}

func TestSupportBundleIsDeterministicAndOmitsCanaries(t *testing.T) {
	clients := supportedClients(t)
	canary := "CANARY-SECRET-private.example.invalid"
	_, _ = clients.Kubernetes.CoreV1().Secrets("default").Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "canary", Namespace: "default"}, Data: map[string][]byte{"password": []byte(canary)}}, metav1.CreateOptions{})
	_, _ = clients.Kubernetes.CoreV1().Events("default").Create(context.Background(), &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "event", Namespace: "default"}, InvolvedObject: corev1.ObjectReference{APIVersion: "networking.waycloak.io/v1beta1", Kind: "VPNGateway", Name: "gateway"}, Reason: "Unavailable", Type: "Warning", Message: canary}, metav1.CreateOptions{})
	first, err := SupportBundle(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	second, err := SupportBundle(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("support bundle is not deterministic for unchanged state")
	}
	contents := untar(t, first)
	if bytes.Contains(contents, []byte(canary)) {
		t.Fatal("support bundle leaked a canary")
	}
	for _, wanted := range []string{"manifest.json", "preflight.json", "doctor.json", "events.json", "Secret objects and data"} {
		if !bytes.Contains(contents, []byte(wanted)) {
			t.Fatalf("bundle lacks %q", wanted)
		}
	}
}

func supportedClients(t *testing.T) *Clients {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}, Spec: corev1.NodeSpec{PodCIDRs: []string{"10.42.0.0/24"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux", KernelVersion: "6.8.0", ContainerRuntimeVersion: "containerd://2.1.0"}}}
	dns := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "kube-dns"}}, Spec: corev1.ServiceSpec{ClusterIP: "10.43.0.10"}}
	kube := kubernetesfake.NewSimpleClientset(node, dns,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "test-cluster-uid"}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "kube-system"}})
	kube.Fake.PrependReactor("create", "secrets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		create := action.(clienttesting.CreateAction)
		secret := create.GetObject().(*corev1.Secret)
		if secret.UID == "" {
			secret.UID = k8stypes.UID("test-secret-" + secret.Namespace + "-" + secret.Name)
		}
		return false, nil, nil
	})
	discovery := &fake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{{GroupVersion: "admissionregistration.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "validatingadmissionpolicies"}, {Name: "mutatingadmissionpolicies"}}}}}, FakedServerVersion: &version.Info{Major: "1", Minor: "36", GitVersion: "v1.36.1+k3s1"}}
	listKinds := map[schema.GroupVersionResource]string{}
	for _, resource := range doctorResources {
		listKinds[resource.GVR] = resource.Kind + "List"
	}
	return &Clients{
		Kubernetes: kube, APIExtensions: apiextensionsfake.NewSimpleClientset(),
		Dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds), Discovery: discovery,
		ClusterServerFingerprint: "sha256:" + strings.Repeat("a", 64), ClusterTrustFingerprint: "sha256:" + strings.Repeat("b", 64),
	}
}

func absentInstallInputs(t *testing.T) (InstalledReleaseObservation, map[string]string) {
	t.Helper()
	source, err := finalizeInstalledReleaseObservation(InstalledReleaseObservation{State: installStateAbsent, CRDIdentities: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	_, identities, _ := testInstallCRDBundle(t)
	return source, identities
}

func testInstallCRDBundle(t *testing.T) ([]*apiextensionsv1.CustomResourceDefinition, map[string]string, []byte) {
	t.Helper()
	identities := make(map[string]string, len(stateCRDNames))
	crds := make([]*apiextensionsv1.CustomResourceDefinition, 0, len(stateCRDNames))
	var bundle bytes.Buffer
	for _, name := range stateCRDNames {
		plural := strings.TrimSuffix(name, ".networking.waycloak.io")
		kind := map[string]string{
			"portforwardleases": "PortForwardLease", "vpnegressroutes": "VPNEgressRoute", "vpngatewayclasses": "VPNGatewayClass",
			"vpngateways": "VPNGateway", "vpnworkloadbindings": "VPNWorkloadBinding", "workloadadapters": "WorkloadAdapter",
		}[plural]
		scope := apiextensionsv1.NamespaceScoped
		if plural == "vpngatewayclasses" {
			scope = apiextensionsv1.ClusterScoped
		}
		crd := &apiextensionsv1.CustomResourceDefinition{
			TypeMeta:   metav1.TypeMeta{APIVersion: apiextensionsv1.SchemeGroupVersion.String(), Kind: "CustomResourceDefinition"},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "networking.waycloak.io", Scope: scope,
				Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: plural, Kind: kind},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1beta1", Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"}}}},
			},
		}
		digest, err := installCRDSpecDigest(crd.Spec)
		if err != nil {
			t.Fatal(err)
		}
		identities[name] = digest
		crds = append(crds, crd)
		data, err := json.Marshal(crd)
		if err != nil {
			t.Fatal(err)
		}
		bundle.Write(data)
		bundle.WriteString("\n---\n")
	}
	return crds, identities, bundle.Bytes()
}

func seedInstalledRelease(t *testing.T, clients *Clients, manifest ReleaseManifest, namespace, release string, revision int64, crds []*apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	ctx := context.Background()
	for _, crd := range crds {
		if _, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			if _, err = clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd.DeepCopy(), metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	releaseSecrets, err := clients.Kubernetes.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{"owner": "helm", "name": release}.AsSelector().String()})
	if err != nil {
		t.Fatal(err)
	}
	for index := range releaseSecrets.Items {
		secret := releaseSecrets.Items[index].DeepCopy()
		secret.Labels["status"] = "superseded"
		if _, err = clients.Kubernetes.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	helmSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "sh.helm.release.v1." + release + ".v" + strconv.FormatInt(revision, 10), Namespace: namespace,
		Labels: map[string]string{"owner": "helm", "name": release, "status": "deployed", "version": strconv.FormatInt(revision, 10)},
	}}
	if _, err = clients.Kubernetes.CoreV1().Secrets(namespace).Create(ctx, helmSecret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	image := func(name string) string { return manifest.Images[name].Repository + "@" + manifest.Images[name].Digest }
	fullname := chartFullname(release)
	controller := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: fullname + "-controller", Namespace: namespace}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "controller", Image: image("replacement-controller"), Args: []string{
			"--release-version=" + manifest.Version, "--release-manifest-digest=" + manifest.ManifestDigest,
			"--gateway-engine-image=" + image("gluetun"), "--gateway-agent-image=" + image("waycloak-gateway-agent"),
		},
	}}}}}}
	if current, getErr := clients.Kubernetes.AppsV1().Deployments(namespace).Get(ctx, controller.Name, metav1.GetOptions{}); getErr == nil {
		controller.ResourceVersion = current.ResourceVersion
		if _, err = clients.Kubernetes.AppsV1().Deployments(namespace).Update(ctx, controller, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else if apierrors.IsNotFound(getErr) {
		if _, err = clients.Kubernetes.AppsV1().Deployments(namespace).Create(ctx, controller, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal(getErr)
	}
	cni := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fullname + "-cni-installer", Namespace: namespace}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "install", Image: image("waycloak-cni"), Args: []string{"install", manifest.Version, manifest.ManifestDigest}}},
		Containers:     []corev1.Container{{Name: "receipt-holder", Image: image("pause")}},
	}}}}
	upsertTestDaemonSet(t, clients, cni)
	agent := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fullname + "-node-agent", Namespace: namespace}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "node-agent", Image: image("waycloak-node-agent"), Args: []string{"--release-version=" + manifest.Version, "--release-manifest-digest=" + manifest.ManifestDigest},
	}}}}}}
	upsertTestDaemonSet(t, clients, agent)
	class := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.waycloak.io/v1beta1", "kind": "VPNGatewayClass",
		"metadata": map[string]any{"name": "gluetun.waycloak.io"},
		"spec":     map[string]any{"releaseIdentity": map[string]any{"version": manifest.Version, "manifestDigest": manifest.ManifestDigest}},
	}}
	if current, getErr := clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, class.GetName(), metav1.GetOptions{}); getErr == nil {
		class.SetUID(current.GetUID())
		class.SetResourceVersion(current.GetResourceVersion())
		class.SetGeneration(current.GetGeneration() + 1)
		if _, err = clients.Dynamic.Resource(gatewayClassGVR).Update(ctx, class, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else if apierrors.IsNotFound(getErr) {
		class.SetUID("test-gateway-class-uid")
		class.SetGeneration(1)
		if _, err = clients.Dynamic.Resource(gatewayClassGVR).Create(ctx, class, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal(getErr)
	}
}

func upsertTestDaemonSet(t *testing.T, clients *Clients, daemonSet *appsv1.DaemonSet) {
	t.Helper()
	ctx := context.Background()
	current, err := clients.Kubernetes.AppsV1().DaemonSets(daemonSet.Namespace).Get(ctx, daemonSet.Name, metav1.GetOptions{})
	if err == nil {
		daemonSet.ResourceVersion = current.ResourceVersion
		if _, err = clients.Kubernetes.AppsV1().DaemonSets(daemonSet.Namespace).Update(ctx, daemonSet, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if _, err = clients.Kubernetes.AppsV1().DaemonSets(daemonSet.Namespace).Create(ctx, daemonSet, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func releaseManifest() ReleaseManifest {
	digest := func(char string) string { return "sha256:" + strings.Repeat(char, 64) }
	manifest := ReleaseManifest{APIVersion: "release.waycloak.io/v1", Version: "v1.0.0-beta.1", Chart: Artifact{Repository: "oci://ghcr.io/amoenus/charts/waycloak", Digest: digest("b")}, Images: map[string]Artifact{"replacement-controller": {Repository: "ghcr.io/amoenus/waycloak-replacement-controller", Digest: digest("c")}, "waycloak-cni": {Repository: "ghcr.io/amoenus/waycloak-cni", Digest: digest("d")}, "waycloak-node-agent": {Repository: "ghcr.io/amoenus/waycloak-node-agent", Digest: digest("e")}, "waycloak-gateway-agent": {Repository: "ghcr.io/amoenus/waycloak-gateway-agent", Digest: digest("f")}, "gluetun": {Repository: "docker.io/qmcgaw/gluetun", Digest: digest("1")}, "pause": {Repository: "registry.k8s.io/pause", Digest: digest("2")}}, Profiles: []string{"networking.waycloak.io/Core-v1"}}
	manifest.ManifestDigest, _ = manifest.IdentityDigest()
	return manifest
}

func checkStatus(report PreflightReport, name string) string {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if strings.Contains(value, wanted) {
			return true
		}
	}
	return false
}
func errorsIsNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}
func untar(t *testing.T, data []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(reader)
	var output bytes.Buffer
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		output.WriteString(header.Name)
		content, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(content)
	}
	return output.Bytes()
}

func TestLoadReleaseManifestRejectsUnknownFields(t *testing.T) {
	manifest := releaseManifest()
	data, _ := json.Marshal(manifest)
	data = bytes.Replace(data, []byte(`{"apiVersion"`), []byte(`{"unknown":true,"apiVersion"`), 1)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadReleaseManifest(path); err == nil {
		t.Fatal("unknown manifest field was accepted")
	}
}
