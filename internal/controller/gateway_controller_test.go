// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"strings"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testDigest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestGatewayReconcilesOwnedResourcesAndObservedStatus(t *testing.T) {
	scheme := gatewayTestScheme(t)
	gateway := controllerTestGateway()
	memberPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "member", Namespace: "apps", UID: types.UID("member-pod-uid")}, Status: corev1.PodStatus{PodIP: "10.42.0.12"}}
	workload := &wayv1.VPNWorkload{ObjectMeta: metav1.ObjectMeta{Name: "pod-member", Namespace: "apps", UID: types.UID("workload-uid")}, Spec: wayv1.VPNWorkloadSpec{PodRef: wayv1.PodReference{Name: memberPod.Name, UID: memberPod.UID}, GatewayRef: wayv1.NamespacedNameReference{Namespace: gateway.Namespace, Name: gateway.Name}}, Status: wayv1.VPNWorkloadStatus{Allocation: wayv1.AllocationStatus{Address: "172.30.99.2"}}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&wayv1.VPNGateway{}, &corev1.Pod{}).WithObjects(gateway, memberPod, workload).Build()
	reconciler := &GatewayReconciler{Client: client, Scheme: scheme, Recorder: record.NewFakeRecorder(10), ManagerImage: "registry.invalid/manager" + testDigest}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	key := types.NamespacedName{Namespace: gateway.Namespace, Name: waygateway.ResourceName(gateway.Name)}
	var service corev1.Service
	if err := client.Get(context.Background(), key, &service); err != nil {
		t.Fatal(err)
	}
	if service.Spec.ClusterIP != corev1.ClusterIPNone || len(service.OwnerReferences) != 1 {
		t.Fatalf("Service ownership/shape = %#v", service)
	}
	var configMap corev1.ConfigMap
	if err := client.Get(context.Background(), key, &configMap); err != nil {
		t.Fatal(err)
	}
	if len(configMap.OwnerReferences) != 1 || configMap.Data[waygateway.EngineAuthKey] == "" {
		t.Fatalf("ConfigMap ownership/shape = %#v", configMap)
	}
	if desired := configMap.Data[waygateway.DesiredStateKey]; !strings.Contains(desired, `"overlayAddress":"172.30.99.2"`) || !strings.Contains(desired, `"underlayIP":"10.42.0.12"`) {
		t.Fatalf("gateway desired state = %s", desired)
	}
	var statefulSet appsv1.StatefulSet
	if err := client.Get(context.Background(), key, &statefulSet); err != nil {
		t.Fatal(err)
	}
	if len(statefulSet.OwnerReferences) != 1 || statefulSet.Spec.Replicas == nil || *statefulSet.Spec.Replicas != 1 {
		t.Fatalf("StatefulSet ownership/shape = %#v", statefulSet)
	}
	var pdb policyv1.PodDisruptionBudget
	if err := client.Get(context.Background(), key, &pdb); err != nil {
		t.Fatal(err)
	}
	if len(pdb.OwnerReferences) != 1 || pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 1 {
		t.Fatalf("PodDisruptionBudget ownership/shape = %#v", pdb)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name + "-0", Namespace: gateway.Namespace, Labels: waygateway.SelectorLabels(gateway), Annotations: map[string]string{waygateway.GatewayNameAnnotation: gateway.Name}},
		Status: corev1.PodStatus{
			Conditions:        []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: waygateway.ManagerContainer, Ready: true}},
		},
	}
	if err := client.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var observed wayv1.VPNGateway
	if err := client.Get(context.Background(), request.NamespacedName, &observed); err != nil {
		t.Fatal(err)
	}
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionScheduled, metav1.ConditionTrue, waystatus.ReasonGatewayPodScheduled)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionTunnelReady, metav1.ConditionTrue, waystatus.ReasonTunnelObservedReady)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionOverlayReady, metav1.ConditionTrue, waystatus.ReasonOverlayObservedReady)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionDNSReady, metav1.ConditionTrue, waystatus.ReasonDNSObservedReady)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionReady, metav1.ConditionTrue, waystatus.ReasonGatewayReady)

	if err := client.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(context.Background(), request.NamespacedName, &observed); err != nil {
		t.Fatal(err)
	}
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionTunnelReady, metav1.ConditionFalse, waystatus.ReasonTunnelNotReady)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionOverlayReady, metav1.ConditionFalse, waystatus.ReasonOverlayNotReady)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionDNSReady, metav1.ConditionFalse, waystatus.ReasonDNSNotReady)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonGatewayComponentsNotReady)
}

func TestGatewayRejectsMutableEngineImageWithoutCreatingResources(t *testing.T) {
	scheme := gatewayTestScheme(t)
	gateway := controllerTestGateway()
	gateway.Spec.Engine.Image = "registry.invalid/engine:latest"
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&wayv1.VPNGateway{}).WithObjects(gateway).Build()
	reconciler := &GatewayReconciler{Client: client, Scheme: scheme, ManagerImage: "registry.invalid/manager" + testDigest}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var observed wayv1.VPNGateway
	if err := client.Get(context.Background(), request.NamespacedName, &observed); err != nil {
		t.Fatal(err)
	}
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionAccepted, metav1.ConditionFalse, waystatus.ReasonInvalidEngineImage)
	var statefulSets appsv1.StatefulSetList
	if err := client.List(context.Background(), &statefulSets); err != nil {
		t.Fatal(err)
	}
	if len(statefulSets.Items) != 0 {
		t.Fatal("mutable engine image produced a gateway workload")
	}
}

func TestGatewayReadyRequiresEnabledComponents(t *testing.T) {
	gateway := controllerTestGateway()
	gateway.Spec.PortForwarding.Enabled = true
	setRemainingGatewayConditions(gateway, true)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionDNSReady, metav1.ConditionTrue, waystatus.ReasonDNSObservedReady)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionPortForwardReady, metav1.ConditionFalse, waystatus.ReasonPortForwardNotImplemented)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonGatewayComponentsNotReady)
}

func gatewayTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, policyv1.AddToScheme, wayv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func controllerTestGateway() *wayv1.VPNGateway {
	return &wayv1.VPNGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "egress", UID: types.UID("gateway-uid")},
		Spec: wayv1.VPNGatewaySpec{
			Engine:   wayv1.EngineSpec{Type: "Test", Image: "registry.invalid/engine" + testDigest},
			Provider: wayv1.ProviderSpec{Name: "test", CredentialsSecretRef: corev1.LocalObjectReference{Name: "credentials"}},
			Overlay:  wayv1.OverlaySpec{CIDR: "172.30.99.0/24", VNI: 7999, MTU: 1320},
		},
	}
}

func assertGatewayCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	condition := apimeta.FindStatusCondition(conditions, conditionType)
	if condition == nil || condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = %#v", conditionType, condition)
	}
}
