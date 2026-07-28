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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func TestInstallPlanHasNoCredentialValuesAndRequiresExactConfirmation(t *testing.T) {
	manifest := releaseManifest()
	report, err := Preflight(context.Background(), supportedClients(t), "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildInstallPlan(manifest, "waycloak-system", "waycloak", report)
	if err != nil {
		t.Fatal(err)
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

func TestInstallApplyCreatesInMemoryTLSAndRejectsTampering(t *testing.T) {
	manifest := releaseManifest()
	report, err := Preflight(context.Background(), supportedClients(t), "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildInstallPlan(manifest, "waycloak-system", "waycloak", report)
	if err != nil {
		t.Fatal(err)
	}
	clients := supportedClients(t)
	called := 0
	runner := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		called++
		if name != "helm" || !containsString(arguments, "@"+plan.Chart.Digest) {
			t.Fatalf("unexpected Helm command: %s %#v", name, arguments)
		}
		return nil, nil
	}
	if err := ApplyInstallPlan(context.Background(), clients, runner, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("Helm was called %d times", called)
	}
	namespace, err := clients.Kubernetes.CoreV1().Namespaces().Get(context.Background(), plan.Namespace, metav1.GetOptions{})
	if err != nil || namespace.Labels["pod-security.kubernetes.io/enforce"] != "privileged" {
		t.Fatalf("reviewed system namespace missing: %#v %v", namespace, err)
	}
	tlsSecret, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(context.Background(), plan.Release+"-observation-tls", metav1.GetOptions{})
	if err != nil || len(tlsSecret.Data["tls.key"]) == 0 {
		t.Fatalf("in-memory TLS identity missing: %v", err)
	}
	tlsSecret.Data["tls.crt"] = []byte("tampered")
	if _, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Update(context.Background(), tlsSecret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyInstallPlan(context.Background(), clients, runner, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("tampered install identity accepted: %v", err)
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
	kube := kubernetesfake.NewSimpleClientset(node, dns, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "kube-system"}})
	discovery := &fake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{{GroupVersion: "admissionregistration.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "validatingadmissionpolicies"}, {Name: "mutatingadmissionpolicies"}}}}}, FakedServerVersion: &version.Info{Major: "1", Minor: "36", GitVersion: "v1.36.1+k3s1"}}
	listKinds := map[schema.GroupVersionResource]string{}
	for _, resource := range doctorResources {
		listKinds[resource.GVR] = resource.Kind + "List"
	}
	return &Clients{Kubernetes: kube, APIExtensions: apiextensionsfake.NewSimpleClientset(), Dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds), Discovery: discovery}
}

func releaseManifest() ReleaseManifest {
	digest := func(char string) string { return "sha256:" + strings.Repeat(char, 64) }
	return ReleaseManifest{APIVersion: "release.waycloak.io/v1", Version: "v1.0.0-beta.1", ManifestDigest: digest("a"), Chart: Artifact{Repository: "oci://ghcr.io/amoenus/charts/waycloak", Digest: digest("b")}, Images: map[string]Artifact{"replacement-controller": {Repository: "ghcr.io/amoenus/waycloak-replacement-controller", Digest: digest("c")}, "waycloak-cni": {Repository: "ghcr.io/amoenus/waycloak-cni", Digest: digest("d")}, "waycloak-node-agent": {Repository: "ghcr.io/amoenus/waycloak-node-agent", Digest: digest("e")}, "waycloak-gateway-agent": {Repository: "ghcr.io/amoenus/waycloak-gateway-agent", Digest: digest("f")}, "gluetun": {Repository: "docker.io/qmcgaw/gluetun", Digest: digest("1")}, "pause": {Repository: "registry.k8s.io/pause", Digest: digest("2")}}, Profiles: []string{"networking.waycloak.io/Core-v1"}}
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
