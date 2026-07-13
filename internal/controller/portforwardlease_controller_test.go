// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/Amoenus/waycloak/internal/delivery"
	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	"github.com/Amoenus/waycloak/internal/provider"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	reconcileLease(t, reconciler, key)
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
	if got.Status.ProviderInternalPort != 1 {
		t.Fatalf("provider internal port = %d", got.Status.ProviderInternalPort)
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
	reconcileLease(t, reconciler, key)
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
	reconcileLease(t, reconciler, key)
	var got wayv1.PortForwardLease
	if err := reconciler.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, got.Status.Conditions, waystatus.ConditionTargetReady, metav1.ConditionFalse, waystatus.ReasonTargetAmbiguous)
}

func TestPortForwardLeasePersistsObservedProviderGeneration(t *testing.T) {
	lease, reconciler := leaseFixture(t, metav1.LabelSelector{MatchLabels: map[string]string{"access": "allowed"}}, 1)
	now := time.Date(2026, 7, 13, 12, 0, 0, 987654321, time.UTC)
	observer := &fakeLeaseObserver{observation: waygateway.PortForwardObservation{Identity: string(lease.UID), InternalPort: 1, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}, PublicPort: 42000, IssuedAt: now, RenewAfter: now.Add(45 * time.Second), ExpiresAt: now.Add(60 * time.Second), Ready: true, GatewayRulesReady: true, GatewayRulesGeneration: 1, TargetAddress: "172.30.99.12", TargetPort: 6881}}
	reconciler.Observer = observer
	deliveryObserver := &fakeDeliveryObserver{observation: delivery.Observation{APIVersion: delivery.APIVersion, Identity: string(lease.UID), PodUID: "pod-uid", Generation: 1, ExpiresAt: now.Add(60 * time.Second).Truncate(time.Second), Ready: true}}
	reconciler.DeliveryObserver = deliveryObserver
	reconciler.Now = func() time.Time { return now }
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "egress"}}
	yes := true
	statefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: waygateway.ResourceName(gateway.Name), Namespace: gateway.Namespace, UID: types.UID("gateway-statefulset-uid")}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "egress", Labels: waygateway.SelectorLabels(gateway), OwnerReferences: []metav1.OwnerReference{{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "StatefulSet", Name: statefulSet.Name, UID: statefulSet.UID, Controller: &yes}}}, Status: corev1.PodStatus{PodIP: "10.42.0.10"}}
	if err := reconciler.Create(context.Background(), statefulSet); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}
	reconcileLease(t, reconciler, key)
	var got wayv1.PortForwardLease
	if err := reconciler.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, got.Status.Conditions, waystatus.ConditionProviderLeaseReady, metav1.ConditionTrue, waystatus.ReasonProviderLeaseObservedReady)
	assertCondition(t, got.Status.Conditions, waystatus.ConditionGatewayRulesReady, metav1.ConditionTrue, waystatus.ReasonGatewayRulesObservedReady)
	assertCondition(t, got.Status.Conditions, waystatus.ConditionDelivered, metav1.ConditionTrue, waystatus.ReasonDeliveryObservedReady)
	assertCondition(t, got.Status.Conditions, waystatus.ConditionReady, metav1.ConditionTrue, waystatus.ReasonLeaseReady)
	if got.Status.PublicPort != 42000 || got.Status.LeaseGeneration != 1 {
		t.Fatalf("provider status = %#v", got.Status)
	}
	if got.Status.ExpiresAt == nil || got.Status.ExpiresAt.Nanosecond() != 0 {
		t.Fatalf("provider expiry was not canonicalized to Kubernetes precision: %#v", got.Status.ExpiresAt)
	}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Get(context.Background(), key, &got); err != nil || got.Status.LeaseGeneration != 1 {
		t.Fatalf("stable generation status=%#v error=%v", got.Status, err)
	}
	observer.observation.PublicPort = 42001
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Get(context.Background(), key, &got); err != nil || got.Status.PublicPort != 42001 || got.Status.LeaseGeneration != 2 {
		t.Fatalf("rotated generation status=%#v error=%v", got.Status, err)
	}
	assertCondition(t, got.Status.Conditions, waystatus.ConditionGatewayRulesReady, metav1.ConditionFalse, waystatus.ReasonGatewayRulesPending)
	assertCondition(t, got.Status.Conditions, waystatus.ConditionDelivered, metav1.ConditionFalse, waystatus.ReasonDeliveryPending)
	observer.observation.GatewayRulesGeneration = 2
	deliveryObserver.observation.Generation = 2
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, got.Status.Conditions, waystatus.ConditionGatewayRulesReady, metav1.ConditionTrue, waystatus.ReasonGatewayRulesObservedReady)
	assertCondition(t, got.Status.Conditions, waystatus.ConditionDelivered, metav1.ConditionTrue, waystatus.ReasonDeliveryObservedReady)
}

func TestPortForwardInternalPortsStayStableAsLeasesChange(t *testing.T) {
	first, reconciler := leaseFixture(t, metav1.LabelSelector{MatchLabels: map[string]string{"access": "allowed"}}, 1)
	firstKey := types.NamespacedName{Namespace: first.Namespace, Name: first.Name}
	reconcileLease(t, reconciler, firstKey)
	second := first.DeepCopy()
	second.Name = "torrent-second"
	second.UID = types.UID("lease-uid-2")
	second.ResourceVersion = ""
	second.Finalizers = nil
	second.Status = wayv1.PortForwardLeaseStatus{}
	if err := reconciler.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	secondKey := types.NamespacedName{Namespace: second.Namespace, Name: second.Name}
	reconcileLease(t, reconciler, secondKey)
	var firstObserved, secondObserved wayv1.PortForwardLease
	if err := reconciler.Get(context.Background(), firstKey, &firstObserved); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Get(context.Background(), secondKey, &secondObserved); err != nil {
		t.Fatal(err)
	}
	if firstObserved.Status.ProviderInternalPort != 1 || secondObserved.Status.ProviderInternalPort != 2 {
		t.Fatalf("internal ports first=%d second=%d", firstObserved.Status.ProviderInternalPort, secondObserved.Status.ProviderInternalPort)
	}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: firstKey}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Get(context.Background(), firstKey, &firstObserved); err != nil || firstObserved.Status.ProviderInternalPort != 1 {
		t.Fatalf("first allocation changed: status=%#v error=%v", firstObserved.Status, err)
	}
}

func TestPortForwardDeletionQuarantinesMappingIdentity(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "deleting", Namespace: "apps", UID: types.UID("lease-uid"), Finalizers: []string{contract.PortForwardLeaseFinalizer}, DeletionTimestamp: &metav1.Time{Time: deletedAt}}, Status: wayv1.PortForwardLeaseStatus{ProviderInternalPort: 1}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&wayv1.PortForwardLease{}).WithObjects(lease).Build()
	now := deletedAt.Add(time.Minute)
	reconciler := &PortForwardLeaseReconciler{Client: client, Now: func() time.Time { return now }}
	key := types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil || result.RequeueAfter != 2*time.Minute {
		t.Fatalf("quarantine result=%#v error=%v", result, err)
	}
	now = deletedAt.Add(4 * time.Minute)
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var current wayv1.PortForwardLease
	if err := reconciler.Get(context.Background(), key, &current); !apierrors.IsNotFound(err) {
		t.Fatalf("lease remained after bounded quarantine: %#v error=%v", current, err)
	}
}

func leaseFixture(t *testing.T, access metav1.LabelSelector, targets int) (*wayv1.PortForwardLease, *PortForwardLeaseReconciler) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"access": "allowed"}}}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "egress"}, Spec: wayv1.VPNGatewaySpec{WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: access}, PortForwarding: wayv1.PortForwardingSpec{Enabled: true}}}
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "torrent", Namespace: "apps", UID: types.UID("lease-uid")}, Spec: wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedNameReference{Namespace: "egress", Name: "private"}, Target: wayv1.PortForwardTargetSpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "torrent"}}, Port: 6881}, Protocols: []wayv1.PortForwardProtocol{wayv1.PortForwardProtocolTCP, wayv1.PortForwardProtocolUDP}}}
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
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps", UID: uid, Labels: map[string]string{"app": "torrent"}, Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}, Status: corev1.PodStatus{PodIP: "10.42.0.20", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
		workload := &wayv1.VPNWorkload{ObjectMeta: metav1.ObjectMeta{Name: contract.WorkloadName(string(uid)), Namespace: "apps"}, Spec: wayv1.VPNWorkloadSpec{PodRef: wayv1.PodReference{Name: name, UID: uid}, GatewayRef: wayv1.NamespacedNameReference{Namespace: "egress", Name: "private"}}, Status: wayv1.VPNWorkloadStatus{Allocation: wayv1.AllocationStatus{Address: address, Generation: 1}}}
		objects = append(objects, pod, workload)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).WithStatusSubresource(&wayv1.PortForwardLease{}).Build()
	return lease, &PortForwardLeaseReconciler{Client: client, Recorder: record.NewFakeRecorder(20)}
}

func reconcileLease(t *testing.T, reconciler *PortForwardLeaseReconciler, key types.NamespacedName) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
}

func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	condition := apiMeta.FindStatusCondition(conditions, conditionType)
	if condition == nil || condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = %#v, want status=%s reason=%s", conditionType, condition, status, reason)
	}
}

type fakeLeaseObserver struct {
	observation waygateway.PortForwardObservation
	err         error
}

func (observer *fakeLeaseObserver) ObserveLease(context.Context, string, string) (waygateway.PortForwardObservation, error) {
	return observer.observation, observer.err
}

type fakeDeliveryObserver struct {
	observation delivery.Observation
	err         error
}

func (observer *fakeDeliveryObserver) ObserveDelivery(context.Context, string, string) (delivery.Observation, error) {
	return observer.observation, observer.err
}
