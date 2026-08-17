// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestControllerActivatesAndDrainsBeforeRollingHandoff(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	lease, gateway, service := leaseFixture()
	gateway.Namespace = lease.Namespace
	lease.Spec.GatewayRef.Namespace = wayv1.NamespaceName(lease.Namespace)
	readyGateway(gateway)
	podA, bindingA := boundPod("app-a", "pod-a", "binding-a", gateway)
	sliceA := endpointSlice("slice-a", "slice-a", service, podA, 8080)
	kube := leaseClient(t, lease, gateway, service, podA, bindingA, sliceA)
	runtime := &fakeRuntime{now: now, withdrawReady: true}
	reconciler := &PortForwardLeaseReconciler{Client: kube, APIReader: kube, Runtime: runtime, Now: func() time.Time { return now }}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(lease)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	current := getLease(t, kube, lease)
	if !containsString(current.Finalizers, ProviderCleanupFinalizer) || current.Status.ActiveEndpoint != nil {
		t.Fatalf("finalizer-before-runtime state = %#v", current)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	current = getLease(t, kube, lease)
	if current.Status.ActiveEndpoint == nil || current.Status.ActiveEndpoint.PodUID != "pod-a" || current.Status.ActiveEndpoint.Phase != wayv1.EndpointPhaseSelecting || current.Status.HandoffGeneration != 1 {
		t.Fatalf("persisted initial selection = %#v", current.Status)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("runtime called before initial selection was durable: %v", runtime.calls)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	current = getLease(t, kube, lease)
	assertReadyLease(t, current, "pod-a", 1)

	sliceA.Endpoints[0].Conditions.Ready = boolPointer(false)
	if err := kube.Update(context.Background(), sliceA); err != nil {
		t.Fatal(err)
	}
	podB, bindingB := boundPod("app-b", "pod-b", "binding-b", gateway)
	sliceB := endpointSlice("slice-b", "slice-b", service, podB, 8080)
	for _, object := range []client.Object{podB, bindingB, sliceB} {
		if err := kube.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	current = getLease(t, kube, lease)
	if current.Status.ActiveEndpoint == nil || current.Status.ActiveEndpoint.PodUID != "pod-b" || current.Status.ActiveEndpoint.Phase != wayv1.EndpointPhaseSelecting || current.Status.HandoffGeneration != 2 {
		t.Fatalf("post-drain selection = %#v", current.Status)
	}
	if got, want := runtime.calls, []string{"reconcile:pod-a:1", "withdraw:pod-a:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("handoff ordering = %v, want %v", got, want)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	current = getLease(t, kube, lease)
	assertReadyLease(t, current, "pod-b", 2)
	if got, want := runtime.calls, []string{"reconcile:pod-a:1", "withdraw:pod-a:1", "reconcile:pod-b:2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("handoff calls = %v, want %v", got, want)
	}
}

func TestControllerHoldsDrainUntilExactWithdrawal(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	lease, gateway, service := leaseFixture()
	gateway.Namespace = lease.Namespace
	lease.Spec.GatewayRef.Namespace = wayv1.NamespaceName(lease.Namespace)
	readyGateway(gateway)
	podA, bindingA := boundPod("app-a", "pod-a", "binding-a", gateway)
	sliceA := endpointSlice("slice-a", "slice-a", service, podA, 8080)
	lease.Finalizers = []string{ProviderCleanupFinalizer}
	lease.Status = wayv1.PortForwardLeaseStatus{ObservedGeneration: 1, HandoffGeneration: 4, ActiveEndpoint: endpointFor(Candidate{ServiceUID: wayv1.ObjectUID(service.UID), EndpointSliceUID: "old-slice", PodUID: "old-pod"}, wayv1.EndpointPhaseActive)}
	kube := leaseClient(t, lease, gateway, service, podA, bindingA, sliceA)
	runtime := &fakeRuntime{now: now, withdrawReady: false}
	reconciler := &PortForwardLeaseReconciler{Client: kube, APIReader: kube, Runtime: runtime, Now: func() time.Time { return now }}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(lease)}); err != nil {
		t.Fatal(err)
	}
	current := getLease(t, kube, lease)
	if current.Status.ActiveEndpoint == nil || current.Status.ActiveEndpoint.PodUID != "old-pod" || current.Status.ActiveEndpoint.Phase != wayv1.EndpointPhaseDraining || current.Status.Provider != nil {
		t.Fatalf("unsafe drain state = %#v", current.Status)
	}
	if len(runtime.calls) != 1 || runtime.calls[0] != "withdraw:old-pod:4" {
		t.Fatalf("runtime calls = %v", runtime.calls)
	}
	if condition := apiMeta.FindStatusCondition(current.Status.Conditions, wayv1.ConditionReady); condition == nil || condition.Status != metav1.ConditionUnknown {
		t.Fatalf("drain readiness = %#v", current.Status.Conditions)
	}
}

func TestApplyStatusRejectsStaleHandoffGeneration(t *testing.T) {
	lease, _, _ := leaseFixture()
	kube := leaseClient(t, lease)
	stale := getLease(t, kube, lease)
	current := getLease(t, kube, lease)
	current.Status.HandoffGeneration = 48
	if err := kube.Status().Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}

	reconciler := &PortForwardLeaseReconciler{Client: kube}
	desired := stale.Status
	desired.HandoffGeneration = 47
	err := reconciler.applyStatus(context.Background(), stale, desired)
	if !apierrors.IsConflict(err) {
		t.Fatalf("stale status update error = %v, want conflict", err)
	}
	if got := getLease(t, kube, lease).Status.HandoffGeneration; got != 48 {
		t.Fatalf("handoff generation regressed to %d", got)
	}
}

func TestControllerDerivesProviderAssignedPortOnlyFromReadyAdapterCapability(t *testing.T) {
	lease, gateway, service := leaseFixture()
	gateway.Namespace = lease.Namespace
	lease.Spec.GatewayRef.Namespace = wayv1.NamespaceName(lease.Namespace)
	lease.Spec.ApplicationAdapterRef = &wayv1.LocalObjectReference{Name: "qbittorrent"}
	readyGateway(gateway)
	gateway.Status.SupportedFeatures = append(gateway.Status.SupportedFeatures, wayv1.FeatureWorkloadAdapter)
	pod, binding := boundPod("app-a", "pod-a", "binding-a", gateway)
	endpointSlice := endpointSlice("slice-a", "slice-a", service, pod, 8080)
	adapter := &wayv1.WorkloadAdapter{ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent", Namespace: lease.Namespace, Generation: 1},
		Spec: wayv1.WorkloadAdapterSpec{Image: "registry.invalid/qbittorrent@sha256:" + strings.Repeat("a", 64), ProtocolVersion: AdapterAPIVersion,
			SupportedApplications: []wayv1.QualifiedName{"example.io/qbittorrent"}, SupportedFeatures: []wayv1.FeatureName{ProviderAssignedApplicationPortFeature}},
		Status: wayv1.WorkloadAdapterStatus{ObservedGeneration: 1, Conditions: wayv1.Conditions{{Type: wayv1.ConditionReady, Status: metav1.ConditionTrue, Reason: wayv1.ReasonReady, ObservedGeneration: 1, LastTransitionTime: metav1.Now()}}}}
	kube := leaseClient(t, lease, gateway, service, pod, binding, endpointSlice, adapter)
	reconciler := &PortForwardLeaseReconciler{Client: kube, APIReader: kube}
	evaluation := reconciler.evaluate(context.Background(), lease)
	if !evaluation.hasSelected || !evaluation.providerAssignedApplicationPort {
		t.Fatalf("adapter capability evaluation = %#v", evaluation)
	}
}

func TestControllerDeletionRecoversGatewayUIDAfterGatewayDisappears(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	lease, gateway, _ := leaseFixture()
	lease.Finalizers = []string{ProviderCleanupFinalizer}
	lease.Status = wayv1.PortForwardLeaseStatus{HandoffGeneration: 3, ActiveEndpoint: &wayv1.ActiveLeaseEndpoint{
		ServiceUID: "service-uid", EndpointSliceUID: "slice-uid", PodUID: "pod-uid", Phase: wayv1.EndpointPhaseActive,
	}, Provider: &wayv1.ProviderMappingStatus{PublicAddress: "8.8.8.8", PublicPort: 42000, ExpiresAt: metav1.NewTime(now.Add(time.Minute))}}
	kube := leaseClient(t, lease, gateway)
	allocator := ProviderPortAllocator{Client: kube, Now: func() time.Time { return now }}
	if _, err := allocator.Reserve(context.Background(), lease, gateway); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	current := getLease(t, kube, lease)
	runtime := &fakeRuntime{now: now, withdrawReady: true}
	reconciler := &PortForwardLeaseReconciler{Client: kube, APIReader: kube, Runtime: runtime, Allocator: allocator, Now: func() time.Time { return now }}
	if _, err := reconciler.cleanup(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if len(runtime.gatewayUIDs) != 1 || runtime.gatewayUIDs[0] != wayv1.ObjectUID(gateway.UID) {
		t.Fatalf("recovered gateway identities = %v", runtime.gatewayUIDs)
	}
}

type fakeRuntime struct {
	now           time.Time
	withdrawReady bool
	calls         []string
	gatewayUIDs   []wayv1.ObjectUID
}

func (f *fakeRuntime) Reconcile(_ context.Context, _ *wayv1.VPNGateway, intent Intent) (Observation, error) {
	if intent.ProviderInternalPort < ProviderPortFirst {
		return Observation{}, fmt.Errorf("provider port was not durably allocated")
	}
	f.calls = append(f.calls, fmt.Sprintf("reconcile:%s:%d", intent.PodUID, intent.HandoffGeneration))
	return Observation{APIVersion: RuntimeAPIVersion, LeaseUID: intent.LeaseUID, GatewayUID: intent.GatewayUID, HandoffGeneration: intent.HandoffGeneration,
		PodUID: intent.PodUID, ObservedAt: f.now, Provider: &ProviderObservation{PublicAddress: netip.MustParseAddr("8.8.8.8"), PublicPort: 42000, ExpiresAt: f.now.Add(time.Minute)},
		GatewayRulesReady: true, Delivered: true, Acknowledged: true}, nil
}

func (f *fakeRuntime) Withdraw(_ context.Context, gateway *wayv1.VPNGateway, intent WithdrawalIntent) (Observation, error) {
	f.gatewayUIDs = append(f.gatewayUIDs, wayv1.ObjectUID(gateway.UID))
	f.calls = append(f.calls, fmt.Sprintf("withdraw:%s:%d", intent.PodUID, intent.HandoffGeneration))
	return Observation{APIVersion: RuntimeAPIVersion, LeaseUID: intent.LeaseUID, GatewayUID: intent.GatewayUID, HandoffGeneration: intent.HandoffGeneration,
		PodUID: intent.PodUID, ObservedAt: f.now, Withdrawn: f.withdrawReady}, nil
}

func (f *fakeRuntime) Quarantine(context.Context, *wayv1.VPNGateway, WithdrawalIntent, time.Time) error {
	return nil
}

func readyGateway(gateway *wayv1.VPNGateway) {
	now := metav1.NewTime(time.Unix(1000, 0).UTC())
	gateway.Status = wayv1.VPNGatewayStatus{ObservedGeneration: gateway.Generation, SupportedFeatures: append(wayv1.BaselineFeatures(), wayv1.FeaturePortForwardSingleActive), Conditions: wayv1.GatewayConditions{
		{Type: wayv1.ConditionAccepted, Status: metav1.ConditionTrue, Reason: wayv1.ReasonAccepted, ObservedGeneration: gateway.Generation, LastTransitionTime: now},
		{Type: wayv1.ConditionReady, Status: metav1.ConditionTrue, Reason: wayv1.ReasonReady, ObservedGeneration: gateway.Generation, LastTransitionTime: now},
	}}
}

func leaseClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, coordinationv1.AddToScheme, discoveryv1.AddToScheme, wayv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&wayv1.PortForwardLease{}).WithObjects(objects...).Build()
}

func getLease(t *testing.T, kube client.Client, lease *wayv1.PortForwardLease) *wayv1.PortForwardLease {
	t.Helper()
	current := &wayv1.PortForwardLease{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(lease), current); err != nil {
		t.Fatal(err)
	}
	return current
}

func assertReadyLease(t *testing.T, lease *wayv1.PortForwardLease, podUID wayv1.ObjectUID, generation int64) {
	t.Helper()
	if lease.Status.ActiveEndpoint == nil || lease.Status.ActiveEndpoint.PodUID != podUID || lease.Status.ActiveEndpoint.Phase != wayv1.EndpointPhaseActive || lease.Status.HandoffGeneration != generation || lease.Status.Provider == nil {
		t.Fatalf("ready lease status = %#v", lease.Status)
	}
	for _, conditionType := range leaseConditionOrder {
		condition := apiMeta.FindStatusCondition(lease.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != lease.Generation {
			t.Fatalf("condition %s = %#v", conditionType, condition)
		}
	}
}
