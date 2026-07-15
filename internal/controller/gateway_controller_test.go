// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
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

type fakeManagerObserver struct {
	observation waygateway.ManagerObservation
	err         error
	endpoint    string
}

func (observer *fakeManagerObserver) Observe(_ context.Context, endpoint string) (waygateway.ManagerObservation, error) {
	observer.endpoint = endpoint
	return observer.observation, observer.err
}

func TestGatewayReconcilesOwnedResourcesAndObservedStatus(t *testing.T) {
	scheme := gatewayTestScheme(t)
	gateway := controllerTestGateway()
	memberPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "member", Namespace: "apps", UID: types.UID("member-pod-uid")}, Status: corev1.PodStatus{PodIP: "10.42.0.12"}}
	workload := &wayv1.VPNWorkload{ObjectMeta: metav1.ObjectMeta{Name: "pod-member", Namespace: "apps", UID: types.UID("workload-uid")}, Spec: wayv1.VPNWorkloadSpec{PodRef: wayv1.PodReference{Name: memberPod.Name, UID: memberPod.UID}, GatewayRef: wayv1.NamespacedNameReference{Namespace: gateway.Namespace, Name: gateway.Name}}, Status: wayv1.VPNWorkloadStatus{Allocation: wayv1.AllocationStatus{Address: "172.30.99.2"}}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&wayv1.VPNGateway{}, &corev1.Pod{}).WithObjects(gateway, memberPod, workload).Build()
	recorder := record.NewFakeRecorder(20)
	observer := &fakeManagerObserver{}
	reconciler := &GatewayReconciler{Client: client, Scheme: scheme, Observer: observer, Recorder: recorder, ManagerImage: "registry.invalid/manager" + testDigest}
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
	observer.observation.AppliedMembershipGeneration = configMap.Data[waygateway.DesiredMembershipGenerationKey]
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
			PodIP:             "10.42.0.20",
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
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionMembershipApplied, metav1.ConditionTrue, waystatus.ReasonMembershipGenerationApplied)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionOverlayReady, metav1.ConditionTrue, waystatus.ReasonOverlayObservedReady)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionDNSReady, metav1.ConditionTrue, waystatus.ReasonDNSObservedReady)
	assertGatewayCondition(t, observed.Status.Conditions, waystatus.ConditionReady, metav1.ConditionTrue, waystatus.ReasonGatewayReady)
	if observed.Status.Overlay.Endpoint != "10.42.0.20:4789" || observed.Status.Overlay.HealthPort != 18080 {
		t.Fatalf("observed overlay status = %#v", observed.Status.Overlay)
	}

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
	if observed.Status.Overlay.Endpoint != "" || observed.Status.Overlay.HealthPort != 0 {
		t.Fatalf("stale overlay status = %#v", observed.Status.Overlay)
	}

	observed.Spec.Engine.Image = "registry.invalid/engine@sha256:" + strings.Repeat("b", 64)
	if err := client.Update(context.Background(), &observed); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	foundRolloutEvent := false
	for !foundRolloutEvent {
		select {
		case event := <-recorder.Events:
			foundRolloutEvent = strings.Contains(event, "GatewayRolloutRequired")
		default:
			t.Fatal("gateway template update did not emit GatewayRolloutRequired")
		}
	}
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
	setRemainingGatewayConditions(gateway, true, true)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionDNSReady, metav1.ConditionTrue, waystatus.ReasonDNSObservedReady)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionPortForwardReady, metav1.ConditionTrue, waystatus.ReasonPortForwardObservedReady)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionReady, metav1.ConditionTrue, waystatus.ReasonGatewayReady)
	setRemainingGatewayConditions(gateway, false, false)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionPortForwardReady, metav1.ConditionFalse, waystatus.ReasonPortForwardNotReady)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonGatewayComponentsNotReady)
}

func TestGatewayStatusWaitsForAppliedMembershipGeneration(t *testing.T) {
	scheme := gatewayTestScheme(t)
	gateway := controllerTestGateway()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: gateway.Namespace, Labels: waygateway.SelectorLabels(gateway)}, Status: corev1.PodStatus{PodIP: "10.42.0.20", Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: waygateway.ManagerContainer, Ready: true}}}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	oldGeneration := waygateway.MembershipGeneration(nil)
	desiredGeneration := waygateway.MembershipGeneration([]waygateway.Member{{ID: "new", OverlayAddress: "172.30.99.2", UnderlayIP: "10.42.0.2"}})
	observer := &fakeManagerObserver{observation: waygateway.ManagerObservation{AppliedMembershipGeneration: oldGeneration}}
	recorder := record.NewFakeRecorder(10)
	reconciler := &GatewayReconciler{Client: client, Observer: observer, Recorder: recorder}
	pending, err := reconciler.observePod(context.Background(), gateway, desiredGeneration)
	if err != nil || !pending {
		t.Fatalf("pending=%v error=%v", pending, err)
	}
	if observer.endpoint != "10.42.0.20:18080" || gateway.Status.Overlay.DesiredMembershipGeneration != desiredGeneration || gateway.Status.Overlay.AppliedMembershipGeneration != oldGeneration {
		t.Fatalf("membership status=%#v endpoint=%q", gateway.Status.Overlay, observer.endpoint)
	}
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionMembershipApplied, metav1.ConditionFalse, waystatus.ReasonMembershipGenerationPending)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonGatewayComponentsNotReady)
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "GatewayMembershipPending") {
			t.Fatalf("pending event = %q", event)
		}
	default:
		t.Fatal("membership transition did not emit an event")
	}
	if _, err := reconciler.observePod(context.Background(), gateway, desiredGeneration); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-recorder.Events:
		t.Fatalf("stable pending state emitted duplicate event %q", event)
	default:
	}
	observer.observation.AppliedMembershipGeneration = desiredGeneration
	pending, err = reconciler.observePod(context.Background(), gateway, desiredGeneration)
	if err != nil || pending {
		t.Fatalf("recovered pending=%v error=%v", pending, err)
	}
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionMembershipApplied, metav1.ConditionTrue, waystatus.ReasonMembershipGenerationApplied)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionReady, metav1.ConditionTrue, waystatus.ReasonGatewayReady)

	observer.err = errors.New("dial timeout after 1 second")
	pending, err = reconciler.observePod(context.Background(), gateway, desiredGeneration)
	if err != nil || !pending {
		t.Fatalf("observation failure pending=%v error=%v", pending, err)
	}
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionMembershipApplied, metav1.ConditionFalse, waystatus.ReasonMembershipObservationFailed)
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "GatewayMembershipObservationFailed") {
			t.Fatalf("observation failure event = %q", event)
		}
	default:
		t.Fatal("observation failure transition did not emit an event")
	}
	observer.err = errors.New("dial timeout after 2 seconds")
	if _, err := reconciler.observePod(context.Background(), gateway, desiredGeneration); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-recorder.Events:
		t.Fatalf("volatile observation error emitted duplicate event %q", event)
	default:
	}
}

func TestGatewayStatusFailsClosedWithoutMembershipObserver(t *testing.T) {
	scheme := gatewayTestScheme(t)
	gateway := controllerTestGateway()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: gateway.Namespace, Labels: waygateway.SelectorLabels(gateway)}, Status: corev1.PodStatus{PodIP: "10.42.0.20", Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: waygateway.ManagerContainer, Ready: true}}}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	reconciler := &GatewayReconciler{Client: client}
	desiredGeneration := waygateway.MembershipGeneration(nil)
	pending, err := reconciler.observePod(context.Background(), gateway, desiredGeneration)
	if err != nil || !pending {
		t.Fatalf("pending=%v error=%v", pending, err)
	}
	if gateway.Status.Overlay.AppliedMembershipGeneration != "" {
		t.Fatalf("unobserved applied generation = %q", gateway.Status.Overlay.AppliedMembershipGeneration)
	}
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionMembershipApplied, metav1.ConditionFalse, waystatus.ReasonMembershipObservationFailed)
	assertGatewayCondition(t, gateway.Status.Conditions, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonGatewayComponentsNotReady)
}

func TestGatewayPublishesOnlyObservedPortForwardLeaseIdentities(t *testing.T) {
	scheme := gatewayTestScheme(t)
	gateway := controllerTestGateway()
	gateway.Spec.PortForwarding = wayv1.PortForwardingSpec{Enabled: true, Driver: "ProtonNatPmp"}
	lease := &wayv1.PortForwardLease{
		ObjectMeta: metav1.ObjectMeta{Name: "torrent", Namespace: "apps", UID: types.UID("lease-uid")},
		Spec:       wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedNameReference{Namespace: gateway.Namespace, Name: gateway.Name}, Protocols: []wayv1.PortForwardProtocol{wayv1.PortForwardProtocolUDP, wayv1.PortForwardProtocolTCP}},
		Status: wayv1.PortForwardLeaseStatus{
			ProviderInternalPort: 7,
			PublicPort:           42000,
			LeaseGeneration:      3,
			Target:               &wayv1.PortForwardTargetStatus{OverlayAddress: "172.30.99.10", Port: 6881},
			Conditions:           []metav1.Condition{{Type: waystatus.ConditionTargetReady, Status: metav1.ConditionTrue, Reason: waystatus.ReasonTargetObservedReady}},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(lease).Build()
	reconciler := &GatewayReconciler{Client: client}
	members := []waygateway.Member{{OverlayAddress: "172.30.99.10"}}
	intents, err := reconciler.portForwardLeases(context.Background(), gateway, members)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].Identity != "lease-uid" || intents[0].InternalPort != 7 || intents[0].SuggestedExternalPort != 42000 || len(intents[0].Protocols) != 2 || intents[0].Protocols[0] != "TCP" || intents[0].TargetAddress != "172.30.99.10" || intents[0].TargetPort != 6881 || intents[0].LeaseGeneration != 3 {
		t.Fatalf("published intents = %#v", intents)
	}
	if err := client.Delete(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	intents, err = reconciler.portForwardLeases(context.Background(), gateway, members)
	if err != nil || len(intents) != 0 {
		t.Fatalf("deleting intents=%#v error=%v", intents, err)
	}
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
