// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	"github.com/Amoenus/waycloak/internal/enrollment"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPodBindingReconcilePersistsExactUIDAllocationBeforeReadiness(t *testing.T) {
	scheme := bindingTestScheme(t)
	now := time.Unix(1000, 0).UTC()
	pod, route, gateway := eligibleBindingObjects(now, "pod-uid")
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, route, gateway).Build()
	reconciler := &PodBindingReconciler{Client: kube, APIReader: kube, Now: func() time.Time { return now }}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}); err != nil {
		t.Fatal(err)
	}

	binding := &wayv1.VPNWorkloadBinding{}
	key := client.ObjectKey{Namespace: pod.Namespace, Name: waybinding.BindingName(pod.UID)}
	if err := kube.Get(context.Background(), key, binding); err != nil {
		t.Fatal(err)
	}
	if binding.Spec.PodRef.UID != wayv1.ObjectUID(pod.UID) || binding.Spec.RouteRef.UID != wayv1.ObjectUID(route.UID) || binding.Spec.GatewayRef.UID != wayv1.ObjectUID(gateway.UID) {
		t.Fatalf("binding refs are not exact: %#v", binding.Spec)
	}
	if binding.Spec.NodeName != wayv1.ObjectName(pod.Spec.NodeName) || binding.Spec.Allocation.Identity == "" || binding.Spec.Allocation.Address == "" {
		t.Fatalf("incomplete binding: %#v", binding.Spec)
	}
	if len(binding.OwnerReferences) != 1 || binding.OwnerReferences[0].UID != pod.UID || !containsString(binding.Finalizers, DataplaneCleanupFinalizer) {
		t.Fatalf("lifecycle metadata = %#v", binding.ObjectMeta)
	}
	leases := &coordinationv1.LeaseList{}
	if err := kube.List(context.Background(), leases, client.InNamespace(gateway.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(leases.Items) != 1 || leases.Items[0].Annotations[waybinding.ReservationPodUIDAnnotation] != string(pod.UID) {
		t.Fatalf("reservations = %#v", leases.Items)
	}
	if binding.Status.ObservedGeneration != 0 || len(binding.Status.Conditions) != 0 {
		t.Fatal("creation incorrectly claimed observed readiness")
	}
}

func TestPodBindingUsesPodUIDNotNameForReuse(t *testing.T) {
	scheme := bindingTestScheme(t)
	now := time.Unix(1000, 0).UTC()
	oldPod, route, gateway := eligibleBindingObjects(now, "old-uid")
	newPod := oldPod.DeepCopy()
	newPod.UID = types.UID("new-uid")
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newPod, route, gateway).Build()
	reconciler := &PodBindingReconciler{Client: kube, APIReader: kube, Now: func() time.Time { return now }}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(newPod)}); err != nil {
		t.Fatal(err)
	}
	if waybinding.BindingName(oldPod.UID) == waybinding.BindingName(newPod.UID) {
		t.Fatal("UID-derived names collided")
	}
	binding := &wayv1.VPNWorkloadBinding{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: newPod.Namespace, Name: waybinding.BindingName(newPod.UID)}, binding); err != nil {
		t.Fatal(err)
	}
	if binding.Spec.PodRef.UID != "new-uid" {
		t.Fatalf("bound Pod UID = %q", binding.Spec.PodRef.UID)
	}
}

func TestPodBindingUpdatesOnlyCredentialFreeNetworkIntent(t *testing.T) {
	scheme := bindingTestScheme(t)
	now := time.Unix(1000, 0).UTC()
	pod, route, gateway := eligibleBindingObjects(now, "pod-uid")
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, route, gateway).Build()
	reconciler := &PodBindingReconciler{Client: kube, APIReader: kube, Now: func() time.Time { return now }}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	binding := &wayv1.VPNWorkloadBinding{}
	key := client.ObjectKey{Namespace: pod.Namespace, Name: waybinding.BindingName(pod.UID)}
	if err := kube.Get(context.Background(), key, binding); err != nil {
		t.Fatal(err)
	}
	allocation := binding.Spec.Allocation

	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(gateway), gateway); err != nil {
		t.Fatal(err)
	}
	gateway.Generation = 2
	gateway.Status.ObservedGeneration = 2
	for i := range gateway.Status.Conditions {
		gateway.Status.Conditions[i].ObservedGeneration = 2
	}
	for i := range gateway.Status.Addresses {
		if gateway.Status.Addresses[i].Type == wayv1.GatewayAddressTypeUnderlayEndpoint {
			gateway.Status.Addresses[i].Value = "198.51.100.3:4789"
		}
	}
	if err := kube.Update(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), key, binding); err != nil {
		t.Fatal(err)
	}
	if binding.Spec.Network.GatewayEndpoint != "198.51.100.3:4789" || binding.Spec.Network.GatewayGeneration != 2 || binding.Spec.Allocation != allocation {
		t.Fatalf("reconfigured binding = %#v", binding.Spec)
	}
}

func TestBindingStatusSeparatesDesiredAppliedAndLive(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	binding := &wayv1.VPNWorkloadBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding", Namespace: "apps", Generation: 7}, Spec: wayv1.VPNWorkloadBindingSpec{
		PodRef: wayv1.LocalUIDReference{Name: "pod", UID: "pod-uid"}, GatewayRef: wayv1.NamespacedUIDReference{Namespace: "network", Name: "gateway", UID: "gateway-uid"}, NodeName: "node-a",
	}}
	reconciler := &VPNWorkloadBindingReconciler{Now: func() time.Time { return now }, ObservationTTL: 30 * time.Second}
	status, _ := reconciler.desiredStatus(binding)
	if conditionStatus(status.Conditions, wayv1.ConditionProgrammed) != metav1.ConditionFalse || conditionStatus(status.Conditions, wayv1.ConditionReady) != metav1.ConditionUnknown {
		t.Fatalf("desired-only status = %#v", status.Conditions)
	}

	binding.Status.AppliedGeneration = 6
	status, _ = reconciler.desiredStatus(binding)
	if apiMeta.FindStatusCondition(status.Conditions, wayv1.ConditionProgrammed).Reason != wayv1.ReasonStaleGeneration {
		t.Fatalf("stale status = %#v", status.Conditions)
	}

	binding.Status.AppliedGeneration = 7
	binding.Status.ObservedPodUID = "wrong-pod"
	binding.Status.ObservedGatewayUID = "gateway-uid"
	binding.Status.Agent = &wayv1.NodeAgentObservation{NodeName: "node-a", NodeBootID: "boot", InstanceID: "agent", ObservedAt: metav1.NewTime(now)}
	status, _ = reconciler.desiredStatus(binding)
	if conditionStatus(status.Conditions, wayv1.ConditionReady) != metav1.ConditionFalse {
		t.Fatalf("identity mismatch status = %#v", status.Conditions)
	}

	binding.Status.ObservedPodUID = "pod-uid"
	status, requeue := reconciler.desiredStatus(binding)
	if conditionStatus(status.Conditions, wayv1.ConditionProgrammed) != metav1.ConditionTrue || conditionStatus(status.Conditions, wayv1.ConditionReady) != metav1.ConditionTrue || requeue != 30*time.Second {
		t.Fatalf("live status = %#v, requeue %s", status.Conditions, requeue)
	}
}

func TestBindingReleaseRequiresFreshExactWithdrawalObservation(t *testing.T) {
	now := time.Unix(3000, 0).UTC()
	binding := &wayv1.VPNWorkloadBinding{Spec: wayv1.VPNWorkloadBindingSpec{
		PodRef: wayv1.LocalUIDReference{UID: "pod-uid"}, GatewayRef: wayv1.NamespacedUIDReference{UID: "gateway-uid"}, NodeName: "node-a",
	}}
	reconciler := &VPNWorkloadBindingReconciler{Now: func() time.Time { return now }, ObservationTTL: 30 * time.Second}
	if reconciler.withdrawalConfirmed(binding) {
		t.Fatal("missing observation confirmed withdrawal")
	}
	binding.Status.ObservedPodUID = "pod-uid"
	binding.Status.ObservedGatewayUID = "gateway-uid"
	binding.Status.Agent = &wayv1.NodeAgentObservation{NodeName: "node-a", NodeBootID: "boot", InstanceID: "agent", ObservedAt: metav1.NewTime(now)}
	if !reconciler.withdrawalConfirmed(binding) {
		t.Fatal("fresh exact zero-applied observation did not confirm withdrawal")
	}
	binding.Status.Agent.ObservedAt = metav1.NewTime(now.Add(-time.Minute))
	if reconciler.withdrawalConfirmed(binding) {
		t.Fatal("stale observation confirmed withdrawal")
	}
	binding.Status.Agent.ObservedAt = metav1.NewTime(now)
	binding.Status.AppliedGeneration = 1
	if reconciler.withdrawalConfirmed(binding) {
		t.Fatal("applied generation confirmed withdrawal")
	}
}

func eligibleBindingObjects(now time.Time, podUID string) (*corev1.Pod, *wayv1.VPNEgressRoute, *wayv1.VPNGateway) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: "apps", UID: types.UID(podUID), Labels: map[string]string{enrollment.RouteLabel: "private"}}, Spec: corev1.PodSpec{NodeName: "node-a"}}
	parent := wayv1.GatewayParentReference{Group: wayv1.GroupName, Kind: "VPNGateway", Namespace: "network", Name: "gateway"}
	route := &wayv1.VPNEgressRoute{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "apps", UID: "route-uid", Generation: 1}, Spec: wayv1.VPNEgressRouteSpec{ParentRefs: []wayv1.GatewayParentReference{parent}}}
	route.Status = wayv1.VPNEgressRouteStatus{ObservedGeneration: 1, Conditions: wayv1.Conditions{trueCondition(wayv1.ConditionReady, 1, now)}, Parents: []wayv1.RouteParentStatus{{ParentRef: parent, ControllerName: RouteControllerName, Conditions: wayv1.Conditions{trueCondition(wayv1.ConditionReady, 1, now)}}}}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: "gateway-uid", Generation: 1}}
	gateway.Spec.ClusterTraffic = wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll}
	gateway.Status = wayv1.VPNGatewayStatus{ObservedGeneration: 1, Addresses: []wayv1.GatewayAddress{
		{Type: wayv1.GatewayAddressTypeOverlayCIDR, Value: "192.0.2.0/29"},
		{Type: wayv1.GatewayAddressTypeOverlayAddress, Value: "192.0.2.1"},
		{Type: wayv1.GatewayAddressTypeUnderlayEndpoint, Value: "198.51.100.2:4789"},
		{Type: wayv1.GatewayAddressTypeOverlayHealthPort, Value: "18080"},
		{Type: wayv1.GatewayAddressTypeVNI, Value: "7999"},
		{Type: wayv1.GatewayAddressTypeMTU, Value: "1320"},
	}, Conditions: wayv1.GatewayConditions{trueCondition(wayv1.ConditionReady, 1, now)}}
	return pod, route, gateway
}

func bindingTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, coordinationv1.AddToScheme, wayv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func trueCondition(conditionType string, generation int64, now time.Time) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: wayv1.ReasonReady, ObservedGeneration: generation, LastTransitionTime: metav1.NewTime(now)}
}
func conditionStatus(conditions wayv1.BindingConditions, conditionType string) metav1.ConditionStatus {
	condition := apiMeta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		return ""
	}
	return condition.Status
}
