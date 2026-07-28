// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

type alphaFixture struct {
	name       string
	plural     string
	kind       string
	scope      apiextensionsv1.ResourceScope
	namespace  string
	instance   string
	group      string
	version    string
	resourceID schema.GroupVersionResource
}

func TestAlphaPurgeInterruptedDrillBeforeFreshReplacement(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_ALPHA_PURGE") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALPHA_PURGE=1 to run the destructive disposable-cluster drill")
	}
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") {
		t.Skip("the destructive drill is restricted to a disposable Kind cluster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	clients, err := waycloakctl.DefaultClientFactory(ctx, "", contextName)
	must(t, err)
	if existing, listErr := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{LabelSelector: "e2e.waycloak.io/alpha-purge=true"}); listErr != nil || len(existing.Items) != 0 {
		t.Fatalf("disposable cluster is not clean: CRDs=%d err=%v", len(existing.Items), listErr)
	}

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	namespace := "alpha-purge-" + suffix
	_, err = clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
	must(t, err)
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=30s").Run()
		for _, fixture := range alphaFixtures(namespace) {
			_ = exec.Command("kubectl", "delete", "crd", fixture.name, "--ignore-not-found", "--wait=true", "--timeout=30s").Run()
		}
	})

	fixtures := alphaFixtures(namespace)
	for _, fixture := range fixtures {
		installAlphaFixture(t, ctx, clients, fixture)
	}
	replicas := int32(0)
	_, err = clients.Kubernetes.AppsV1().Deployments(namespace).Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "protected-owner", Namespace: namespace},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "protected-owner"}}, Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "protected-owner"}, Annotations: map[string]string{"networking.waycloak.io/gateway": "old"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}},
		}},
	}, metav1.CreateOptions{})
	must(t, err)

	plan, err := waycloakctl.BuildAlphaPurgePlan(ctx, clients)
	must(t, err)
	if len(plan.Targets) != 8 || len(plan.ProtectedWorkloadOwners) != 1 || len(plan.ProtectedPods) != 0 {
		t.Fatalf("unexpected exact plan inventory: targets=%d owners=%d pods=%d", len(plan.Targets), len(plan.ProtectedWorkloadOwners), len(plan.ProtectedPods))
	}
	encoded, err := json.Marshal(plan)
	must(t, err)
	for _, forbidden := range []string{"credential-canary", "private-endpoint-canary"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("purge plan disclosed %q", forbidden)
		}
	}

	protectedPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected-process", Namespace: namespace, Annotations: map[string]string{"networking.waycloak.io/gateway": "old"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}}
	_, err = clients.Kubernetes.CoreV1().Pods(namespace).Create(ctx, protectedPod, metav1.CreateOptions{})
	must(t, err)
	if _, err = waycloakctl.ApplyAlphaPurgePlan(ctx, clients, plan, plan.PlanID, "protected-runtime-empty", "alpha-runtime-uninstalled"); err == nil || !strings.Contains(err.Error(), "protected Pods still exist") {
		t.Fatalf("purge did not refuse a surviving protected Pod: %v", err)
	}
	must(t, clients.Kubernetes.CoreV1().Pods(namespace).Delete(ctx, protectedPod.Name, metav1.DeleteOptions{}))
	must(t, wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		_, getErr := clients.Kubernetes.CoreV1().Pods(namespace).Get(ctx, protectedPod.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(getErr), nil
	}))

	gatewayFixture := fixtures[1]
	gateway, err := clients.Dynamic.Resource(gatewayFixture.resourceID).Namespace(namespace).Get(ctx, gatewayFixture.instance, metav1.GetOptions{})
	must(t, err)
	gateway.SetFinalizers([]string{"networking.waycloak.io/provider-lease-quarantine"})
	_, err = clients.Dynamic.Resource(gatewayFixture.resourceID).Namespace(namespace).Update(ctx, gateway, metav1.UpdateOptions{})
	must(t, err)
	if _, err = waycloakctl.ApplyAlphaPurgePlan(ctx, clients, plan, plan.PlanID, "protected-runtime-empty", "alpha-runtime-uninstalled"); err == nil || !strings.Contains(err.Error(), "controller finalizers") {
		t.Fatalf("purge did not refuse a controller finalizer: %v", err)
	}
	gateway, err = clients.Dynamic.Resource(gatewayFixture.resourceID).Namespace(namespace).Get(ctx, gatewayFixture.instance, metav1.GetOptions{})
	must(t, err)
	gateway.SetFinalizers(nil)
	_, err = clients.Dynamic.Resource(gatewayFixture.resourceID).Namespace(namespace).Update(ctx, gateway, metav1.UpdateOptions{})
	must(t, err)

	drift := alphaInstance(gatewayFixture, "post-plan-target")
	_, err = clients.Dynamic.Resource(gatewayFixture.resourceID).Namespace(namespace).Create(ctx, drift, metav1.CreateOptions{})
	must(t, err)
	if _, err = waycloakctl.ApplyAlphaPurgePlan(ctx, clients, plan, plan.PlanID, "protected-runtime-empty", "alpha-runtime-uninstalled"); err == nil || !strings.Contains(err.Error(), "target set changed") {
		t.Fatalf("purge did not refuse target drift: %v", err)
	}
	must(t, clients.Dynamic.Resource(gatewayFixture.resourceID).Namespace(namespace).Delete(ctx, drift.GetName(), metav1.DeleteOptions{}))

	partial := fixtures[0]
	partialObject, err := clients.Dynamic.Resource(partial.resourceID).Namespace(namespace).Get(ctx, partial.instance, metav1.GetOptions{})
	must(t, err)
	partialUID := partialObject.GetUID()
	must(t, clients.Dynamic.Resource(partial.resourceID).Namespace(namespace).Delete(ctx, partial.instance, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &partialUID}}))

	report, err := waycloakctl.ApplyAlphaPurgePlan(ctx, clients, plan, plan.PlanID, "protected-runtime-empty", "alpha-runtime-uninstalled")
	must(t, err)
	if !report.Complete || report.DeletedInstances != 3 || report.DeletedCRDs != 4 {
		t.Fatalf("interrupted purge did not converge exactly: %#v", report)
	}
	for _, fixture := range fixtures {
		_, getErr := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, fixture.name, metav1.GetOptions{})
		if !apierrors.IsNotFound(getErr) {
			t.Fatalf("alpha CRD %s remains after purge: %v", fixture.name, getErr)
		}
	}
	assertCommandFails(t, "alpha discovery remained served after exact purge", nil, "kubectl", "get", "--raw", "/apis/networking.waycloak.io/v1alpha1")
}

func alphaFixtures(namespace string) []alphaFixture {
	values := []alphaFixture{
		{name: "portforwardleases.networking.waycloak.io", plural: "portforwardleases", kind: "PortForwardLease", scope: apiextensionsv1.NamespaceScoped, namespace: namespace, instance: "lease"},
		{name: "vpngateways.networking.waycloak.io", plural: "vpngateways", kind: "VPNGateway", scope: apiextensionsv1.NamespaceScoped, namespace: namespace, instance: "gateway"},
		{name: "vpnworkloads.networking.waycloak.io", plural: "vpnworkloads", kind: "VPNWorkload", scope: apiextensionsv1.NamespaceScoped, namespace: namespace, instance: "workload"},
		{name: "workloadadapters.networking.waycloak.io", plural: "workloadadapters", kind: "WorkloadAdapter", scope: apiextensionsv1.ClusterScoped, instance: "adapter"},
	}
	for index := range values {
		values[index].group = "networking.waycloak.io"
		values[index].version = "v1alpha1"
		values[index].resourceID = schema.GroupVersionResource{Group: values[index].group, Version: values[index].version, Resource: values[index].plural}
	}
	return values
}

func installAlphaFixture(t *testing.T, ctx context.Context, clients *waycloakctl.Clients, fixture alphaFixture) {
	t.Helper()
	preserve := true
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: fixture.name, Labels: map[string]string{"e2e.waycloak.io/alpha-purge": "true"}},
		Spec:       apiextensionsv1.CustomResourceDefinitionSpec{Group: fixture.group, Scope: fixture.scope, Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: fixture.plural, Singular: strings.ToLower(fixture.kind), Kind: fixture.kind, ListKind: fixture.kind + "List"}, Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: fixture.version, Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: &preserve}}}}},
	}
	_, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
	must(t, err)
	must(t, wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		current, getErr := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, fixture.name, metav1.GetOptions{})
		if getErr != nil {
			return false, getErr
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	}))
	namespaceable := clients.Dynamic.Resource(fixture.resourceID)
	var resource dynamic.ResourceInterface = namespaceable
	if fixture.scope == apiextensionsv1.NamespaceScoped {
		resource = namespaceable.Namespace(fixture.namespace)
	}
	_, err = resource.Create(ctx, alphaInstance(fixture, fixture.instance), metav1.CreateOptions{})
	must(t, err)
}

func alphaInstance(fixture alphaFixture, name string) *unstructured.Unstructured {
	metadata := map[string]any{"name": name}
	if fixture.scope == apiextensionsv1.NamespaceScoped {
		metadata["namespace"] = fixture.namespace
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": fixture.group + "/" + fixture.version,
		"kind":       fixture.kind,
		"metadata":   metadata,
		"spec":       map[string]any{"credential": "credential-canary", "endpoint": "private-endpoint-canary"},
	}}
}
