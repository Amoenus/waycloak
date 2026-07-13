// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPortForwardLeaseObservesUIDBoundTargetWithoutClaimingProviderReadiness(t *testing.T) {
	lease, reconciler := leaseFixture(t, metav1.LabelSelector{MatchLabels: map[string]string{"access": "allowed"}}, 1)
	key := types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got wayv1.PortForwardLease
	if err := reconciler.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, got.Status.Conditions, waystatus.ConditionAccepted, metav1.ConditionTrue, waystatus.ReasonLeaseAccepted)
	assertCondition(t, got.Status.Conditions, waystatus.ConditionTargetReady, metav1.ConditionTrue, waystatus.ReasonTargetObservedReady)
	assertCondition(t, got.Status.Conditions, waystatus.ConditionProviderLeaseReady, metav1.ConditionFalse, waystatus.ReasonProviderLeasePending)
	assertCondition(t, got.Status.Conditions, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonLeaseComponentsNotReady)
	if got.Status.Target == nil || got.Status.Target.PodRef.UID != "pod-uid" || got.Status.Target.OverlayAddress != "172.30.99.12" || got.Status.Target.Port != 6881 {
		t.Fatalf("target status = %#v", got.Status.Target)
	}
	before := got.Status.DeepCopy()
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(*before, got.Status) {
		t.Fatalf("idempotent reconcile changed status: before=%#v after=%#v", *before, got.Status)
	}
}

func TestPortForwardLeaseRejectsUnauthorizedGateway(t *testing.T) {
	lease, reconciler := leaseFixture(t, metav1.LabelSelector{MatchLabels: map[string]string{"other": "yes"}}, 1)
	key := types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got wayv1.PortForwardLease
	if err := reconciler.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, got.Status.Conditions, waystatus.ConditionAccepted, metav1.ConditionFalse, waystatus.ReasonUnauthorizedGateway)
	if got.Status.Target != nil {
		t.Fatalf("unauthorized lease exposed target: %#v", got.Status.Target)
	}
}

func TestPortForwardLeaseRejectsAmbiguousReadyTargets(t *testing.T) {
	lease, reconciler := leaseFixture(t, metav1.LabelSelector{MatchLabels: map[string]string{"access": "allowed"}}, 2)
	key := types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got wayv1.PortForwardLease
	if err := reconciler.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, got.Status.Conditions, waystatus.ConditionTargetReady, metav1.ConditionFalse, waystatus.ReasonTargetAmbiguous)
}

func leaseFixture(t *testing.T, access metav1.LabelSelector, targets int) (*wayv1.PortForwardLease, *PortForwardLeaseReconciler) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"access": "allowed"}}}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "egress"}, Spec: wayv1.VPNGatewaySpec{WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: access}, PortForwarding: wayv1.PortForwardingSpec{Enabled: true}}}
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "torrent", Namespace: "apps"}, Spec: wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedNameReference{Namespace: "egress", Name: "private"}, Target: wayv1.PortForwardTargetSpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "torrent"}}, Port: 6881}, Protocols: []wayv1.PortForwardProtocol{wayv1.PortForwardProtocolTCP, wayv1.PortForwardProtocolUDP}}}
	objects := []runtime.Object{namespace, gateway, lease}
	for i := 0; i < targets; i++ {
		uid := types.UID("pod-uid")
		name := "torrent-0"
		address := "172.30.99.12"
		if i > 0 {
			uid = types.UID("pod-uid-2")
			name = "torrent-1"
			address = "172.30.99.13"
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps", UID: uid, Labels: map[string]string{"app": "torrent"}, Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
		workload := &wayv1.VPNWorkload{ObjectMeta: metav1.ObjectMeta{Name: contract.WorkloadName(string(uid)), Namespace: "apps"}, Spec: wayv1.VPNWorkloadSpec{PodRef: wayv1.PodReference{Name: name, UID: uid}, GatewayRef: wayv1.NamespacedNameReference{Namespace: "egress", Name: "private"}}, Status: wayv1.VPNWorkloadStatus{Allocation: wayv1.AllocationStatus{Address: address, Generation: 1}}}
		objects = append(objects, pod, workload)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).WithStatusSubresource(&wayv1.PortForwardLease{}).Build()
	return lease, &PortForwardLeaseReconciler{Client: client, Recorder: record.NewFakeRecorder(20)}
}

func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	condition := apiMeta.FindStatusCondition(conditions, conditionType)
	if condition == nil || condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = %#v, want status=%s reason=%s", conditionType, condition, status, reason)
	}
}
