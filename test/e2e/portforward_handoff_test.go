// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	wayportforward "github.com/Amoenus/waycloak/internal/portforward"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyPortForwardSingleActiveHandoff(t *testing.T, namespace, serviceAccount string) {
	t.Helper()
	ctx := context.Background()
	config, err := ctrl.GetConfig()
	must(t, err)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{wayv1.AddToScheme, corev1.AddToScheme, coordinationv1.AddToScheme, discoveryv1.AddToScheme} {
		must(t, add(scheme))
	}
	admin, err := ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
	must(t, err)
	controllerConfig := rest.CopyConfig(config)
	controllerConfig.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + namespace + ":" + serviceAccount,
		Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"}}
	controller, err := ctrlclient.New(controllerConfig, ctrlclient.Options{Scheme: scheme})
	must(t, err)

	gateway := &wayv1.VPNGateway{}
	must(t, admin.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: "private"}, gateway))
	gateway.Status.ObservedGeneration = gateway.Generation
	gateway.Status.SupportedFeatures = append(wayv1.CoreFeatures(), wayv1.FeaturePortForwardSingleActive)
	gateway.Status.Conditions = currentE2EConditions(gateway.Generation, wayv1.ConditionAccepted, wayv1.ConditionReady)
	must(t, admin.Status().Update(ctx, gateway))

	pods := make([]*corev1.Pod, 0, 2)
	for _, name := range []string{"protected", "binding-peer"} {
		pod := waitForScheduledPod(t, ctx, admin, ctrlclient.ObjectKey{Namespace: namespace, Name: name})
		waitForPodReady(t, admin, pod)
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(pod), pod))
		binding := &wayv1.VPNWorkloadBinding{}
		must(t, controller.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: waybinding.BindingName(pod.UID)}, binding))
		binding.Status.ObservedGeneration = binding.Generation
		binding.Status.Conditions = wayv1.BindingConditions{
			currentE2ECondition(wayv1.ConditionProgrammed, wayv1.ReasonProgrammed, binding.Generation),
			currentE2ECondition(wayv1.ConditionReady, wayv1.ReasonReady, binding.Generation),
			currentE2ECondition(wayv1.ConditionNodeReady, wayv1.ReasonNodeReady, binding.Generation),
		}
		must(t, controller.Status().Update(ctx, binding))
		pods = append(pods, pod)
	}

	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "single-active", Namespace: namespace}, Spec: corev1.ServiceSpec{
		Ports: []corev1.ServicePort{{Name: "peer", Port: 6881, TargetPort: intstr.FromInt32(6881), Protocol: corev1.ProtocolTCP}},
	}}
	must(t, admin.Create(ctx, service))
	ready := true
	controllerOwner := true
	port := int32(6881)
	portName := "peer"
	endpointSlice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "single-active", Namespace: namespace,
		Labels: map[string]string{discoveryv1.LabelServiceName: service.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID, Controller: &controllerOwner}}},
		AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &portName, Port: &port}},
		Endpoints: []discoveryv1.Endpoint{exactE2EEndpoint(pods[0], &ready)},
	}
	must(t, admin.Create(ctx, endpointSlice))
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "single-active", Namespace: namespace}, Spec: wayv1.PortForwardLeaseSpec{
		GatewayRef: wayv1.NamespacedObjectReference{Namespace: wayv1.NamespaceName(namespace), Name: "private"},
		BackendRef: wayv1.ServiceBackendReference{Group: "", Kind: "Service", Name: wayv1.ObjectName(service.Name), Port: intstr.FromString("peer")},
		Protocols:  []wayv1.TransportProtocol{wayv1.ProtocolTCP, wayv1.ProtocolUDP}, EndpointPolicy: wayv1.EndpointPolicySingleActive,
	}}
	must(t, admin.Create(ctx, lease))

	now := time.Now().UTC()
	runtimeBackend := &e2ePortForwardRuntime{now: now}
	reconciler := &wayportforward.PortForwardLeaseReconciler{Client: admin, APIReader: admin, Runtime: runtimeBackend, Now: func() time.Time { return now }}
	request := ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(lease)}
	_, err = reconciler.Reconcile(ctx, request)
	must(t, err)
	_, err = reconciler.Reconcile(ctx, request)
	must(t, err)
	must(t, admin.Get(ctx, request.NamespacedName, lease))
	assertE2EActiveLease(t, lease, wayv1.ObjectUID(pods[0].UID), 1)

	must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(endpointSlice), endpointSlice))
	endpointSlice.Endpoints = []discoveryv1.Endpoint{exactE2EEndpoint(pods[1], &ready)}
	must(t, admin.Update(ctx, endpointSlice))
	_, err = reconciler.Reconcile(ctx, request)
	must(t, err)
	must(t, admin.Get(ctx, request.NamespacedName, lease))
	if lease.Status.ActiveEndpoint == nil || lease.Status.ActiveEndpoint.PodUID != wayv1.ObjectUID(pods[1].UID) || lease.Status.ActiveEndpoint.Phase != wayv1.EndpointPhaseSelecting {
		t.Fatalf("post-withdrawal successor = %#v", lease.Status.ActiveEndpoint)
	}
	wantCalls := []string{fmt.Sprintf("reconcile:%s:1", pods[0].UID), fmt.Sprintf("withdraw:%s:1", pods[0].UID)}
	if !reflect.DeepEqual(runtimeBackend.calls, wantCalls) {
		t.Fatalf("unsafe handoff order = %v, want %v", runtimeBackend.calls, wantCalls)
	}
	_, err = reconciler.Reconcile(ctx, request)
	must(t, err)
	must(t, admin.Get(ctx, request.NamespacedName, lease))
	assertE2EActiveLease(t, lease, wayv1.ObjectUID(pods[1].UID), 2)
	wantCalls = append(wantCalls, fmt.Sprintf("reconcile:%s:2", pods[1].UID))
	if !reflect.DeepEqual(runtimeBackend.calls, wantCalls) {
		t.Fatalf("handoff calls = %v, want %v", runtimeBackend.calls, wantCalls)
	}
}

func exactE2EEndpoint(pod *corev1.Pod, ready *bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{Addresses: []string{pod.Status.PodIP}, Conditions: discoveryv1.EndpointConditions{Ready: ready, Serving: ready},
		TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID}}
}

func currentE2EConditions(generation int64, conditionTypes ...string) wayv1.GatewayConditions {
	conditions := make(wayv1.GatewayConditions, 0, len(conditionTypes))
	for _, conditionType := range conditionTypes {
		conditions = append(conditions, currentE2ECondition(conditionType, conditionType, generation))
	}
	return conditions
}

func currentE2ECondition(conditionType, reason string, generation int64) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: reason, Message: "Observed by the E2E fixture",
		ObservedGeneration: generation, LastTransitionTime: metav1.Now()}
}

func assertE2EActiveLease(t *testing.T, lease *wayv1.PortForwardLease, podUID wayv1.ObjectUID, generation int64) {
	t.Helper()
	if lease.Status.ActiveEndpoint == nil || lease.Status.ActiveEndpoint.PodUID != podUID || lease.Status.ActiveEndpoint.Phase != wayv1.EndpointPhaseActive || lease.Status.HandoffGeneration != generation {
		t.Fatalf("active lease = %#v", lease.Status)
	}
	for _, conditionType := range []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady, wayv1.ConditionGatewayRulesReady, wayv1.ConditionDelivered, wayv1.ConditionAcknowledged} {
		condition := apiMeta.FindStatusCondition(lease.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != lease.Generation {
			t.Fatalf("lease condition %s = %#v", conditionType, condition)
		}
	}
}

type e2ePortForwardRuntime struct {
	now   time.Time
	calls []string
}

func (r *e2ePortForwardRuntime) Reconcile(_ context.Context, _ *wayv1.VPNGateway, intent wayportforward.Intent) (wayportforward.Observation, error) {
	r.calls = append(r.calls, fmt.Sprintf("reconcile:%s:%d", intent.PodUID, intent.HandoffGeneration))
	return wayportforward.Observation{APIVersion: wayportforward.RuntimeAPIVersion, LeaseUID: intent.LeaseUID, GatewayUID: intent.GatewayUID,
		HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID, ObservedAt: r.now,
		Provider:          &wayportforward.ProviderObservation{PublicAddress: netip.MustParseAddr("198.51.100.10"), PublicPort: 42000, ExpiresAt: r.now.Add(time.Minute)},
		GatewayRulesReady: true, Delivered: true, Acknowledged: true}, nil
}

func (r *e2ePortForwardRuntime) Withdraw(_ context.Context, _ *wayv1.VPNGateway, intent wayportforward.WithdrawalIntent) (wayportforward.Observation, error) {
	r.calls = append(r.calls, fmt.Sprintf("withdraw:%s:%d", intent.PodUID, intent.HandoffGeneration))
	return wayportforward.Observation{APIVersion: wayportforward.RuntimeAPIVersion, LeaseUID: intent.LeaseUID, GatewayUID: intent.GatewayUID,
		HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID, ObservedAt: r.now, Withdrawn: true}, nil
}

func (*e2ePortForwardRuntime) Quarantine(context.Context, *wayv1.VPNGateway, wayportforward.WithdrawalIntent, time.Time) error {
	return nil
}
