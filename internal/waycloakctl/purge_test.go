// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

var testAlphaGVR = schema.GroupVersionResource{Group: alphaGroup, Version: "v1alpha1", Resource: "vpngateways"}

func TestAlphaPurgePlanIsExactAndRedactsObjectContent(t *testing.T) {
	clients := purgeTestClients(t, true)
	plan, err := BuildAlphaPurgePlan(context.Background(), clients)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets) != 2 || len(plan.ProtectedWorkloadOwners) != 1 || len(plan.ProtectedPods) != 1 {
		t.Fatalf("unexpected inventory: %#v", plan)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"credential-canary", "private-endpoint-canary", "https://private.invalid"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("purge plan disclosed object content %q", forbidden)
		}
	}
	if err := plan.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAlphaPurgeApplyRequiresEmptyRuntimeAndDeletesOnlyExactTargets(t *testing.T) {
	clients := purgeTestClients(t, false)
	plan, err := BuildAlphaPurgePlan(context.Background(), clients)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyAlphaPurgePlan(context.Background(), clients, plan, "wrong", runtimeEmptyAttestation, runtimeGoneAttestation); err == nil {
		t.Fatal("wrong confirmation was accepted")
	}
	report, err := ApplyAlphaPurgePlan(context.Background(), clients, plan, plan.PlanID, runtimeEmptyAttestation, runtimeGoneAttestation)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || report.DeletedInstances != 1 || report.DeletedCRDs != 1 {
		t.Fatalf("unexpected purge report: %#v", report)
	}
	if _, err = clients.Dynamic.Resource(testAlphaGVR).Namespace("apps").Get(context.Background(), "gateway", metav1.GetOptions{}); !apiIsNotFound(err) {
		t.Fatalf("alpha instance still exists: %v", err)
	}
}

func TestAlphaPurgeApplyRefusesRemainingPodAndTargetDrift(t *testing.T) {
	clients := purgeTestClients(t, true)
	plan, err := BuildAlphaPurgePlan(context.Background(), clients)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyAlphaPurgePlan(context.Background(), clients, plan, plan.PlanID, runtimeEmptyAttestation, runtimeGoneAttestation); err == nil || !strings.Contains(err.Error(), "protected Pods still exist") {
		t.Fatalf("remaining Pod was not rejected: %v", err)
	}
	if err = clients.Kubernetes.CoreV1().Pods("apps").Delete(context.Background(), "protected", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	drift := alphaObject("other", types.UID("other-uid"))
	if _, err = clients.Dynamic.Resource(testAlphaGVR).Namespace("apps").Create(context.Background(), drift, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyAlphaPurgePlan(context.Background(), clients, plan, plan.PlanID, runtimeEmptyAttestation, runtimeGoneAttestation); err == nil || !strings.Contains(err.Error(), "target set changed") {
		t.Fatalf("target drift was not rejected: %v", err)
	}
}

func purgeTestClients(t *testing.T, protectedPod bool) *Clients {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "vpngateways.networking.waycloak.io", UID: types.UID("crd-uid")}, Spec: apiextensionsv1.CustomResourceDefinitionSpec{
		Group: alphaGroup, Scope: apiextensionsv1.NamespaceScoped, Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "vpngateways", Kind: "VPNGateway"},
		Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true}},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{testAlphaGVR: "VPNGatewayList"})
	if _, err := dynamicClient.Resource(testAlphaGVR).Namespace("apps").Create(context.Background(), alphaObject("gateway", types.UID("gateway-uid")), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	objects := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("cluster-uid")}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: "apps", UID: types.UID("deployment-uid")}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"networking.waycloak.io/gateway": "gateway"}}}}},
	}
	if protectedPod {
		objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: "apps", UID: types.UID("pod-uid"), Annotations: map[string]string{"internal.networking.waycloak.io/injection-version": "v1alpha2"}}})
	}
	return &Clients{Kubernetes: kubernetesfake.NewSimpleClientset(objects...), APIExtensions: extensionsfake.NewSimpleClientset(crd), Dynamic: dynamicClient,
		ClusterServerFingerprint: fingerprintText("https://private.invalid"), ClusterTrustFingerprint: fingerprintText("test-ca")}
}

func alphaObject(name string, uid types.UID) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": alphaGroup + "/v1alpha1", "kind": "VPNGateway",
		"metadata": map[string]any{"name": name, "namespace": "apps", "uid": string(uid)},
		"spec":     map[string]any{"credential": "credential-canary", "endpoint": "https://private.invalid"},
		"status":   map[string]any{"providerEndpoint": "private-endpoint-canary"},
	}}
}

func apiIsNotFound(err error) bool { return err != nil && strings.Contains(err.Error(), "not found") }
