// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package observability

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	"github.com/Amoenus/waycloak/internal/enrollment"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCollectorProjectsBoundedAggregateState(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	gateway := &wayv1.VPNGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "private-gateway", Namespace: "tenant-secret", UID: "gateway-uid", Generation: 3},
		Status: wayv1.VPNGatewayStatus{Conditions: wayv1.GatewayConditions{
			{Type: wayv1.ConditionReady, Status: metav1.ConditionFalse, Reason: wayv1.ReasonNotReady, ObservedGeneration: 3, LastTransitionTime: now},
			{Type: wayv1.ConditionTunnelReady, Status: metav1.ConditionFalse, Reason: wayv1.ReasonTunnelNotReady, ObservedGeneration: 3, LastTransitionTime: now},
			{Type: wayv1.ConditionDNSReady, Status: metav1.ConditionUnknown, Reason: wayv1.ReasonObservationUnavailable, ObservedGeneration: 2, LastTransitionTime: now},
		}},
	}
	podUID := types.UID("pod-sensitive-uid")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sensitive-workload", Namespace: "tenant-secret", UID: podUID, Labels: map[string]string{enrollment.RouteLabel: "private-route"}}, Spec: corev1.PodSpec{NodeName: "secret-node"}}
	binding := &wayv1.VPNWorkloadBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "binding-sensitive", Namespace: pod.Namespace, UID: "binding-uid", Generation: 4},
		Spec:       wayv1.VPNWorkloadBindingSpec{PodRef: wayv1.LocalUIDReference{Name: wayv1.ObjectName(pod.Name), UID: wayv1.ObjectUID(podUID)}},
		Status:     wayv1.VPNWorkloadBindingStatus{Conditions: wayv1.BindingConditions{{Type: wayv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "credential-looking-provider-response", ObservedGeneration: 4, LastTransitionTime: now}}},
	}
	reservation := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name: "reservation-sensitive", Namespace: pod.Namespace,
		Labels:      map[string]string{waybinding.ReservationManagedByLabel: waybinding.ReservationManagedByValue},
		Annotations: map[string]string{waybinding.ReservationStateAnnotation: waybinding.ReservationStateQuarantined, waybinding.ReservationAddressAnnotation: "10.0.0.2/32"},
	}}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway, pod, binding, reservation).Build()
	collector := NewCollector(reader)

	text := gatherText(t, registerCollector(t, collector))
	for _, want := range []string{
		`waycloak_resources{resource="vpngateway"} 1`,
		`condition="TunnelReady",current="true",reason="TunnelNotReady",resource="vpngateway",status="False"`,
		`condition="DNSReady",current="false",reason="ObservationUnavailable",resource="vpngateway",status="Unknown"`,
		`condition="Ready",current="true",reason="Other",resource="vpnworkloadbinding",status="False"`,
		`waycloak_enrolled_pods{state="fail_closed"} 1`,
		`waycloak_workload_allocations{state="quarantined"} 1`,
		`waycloak_metrics_collection_success{source="pods"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics do not contain %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"tenant-secret", "private-gateway", "sensitive-workload", "pod-sensitive-uid", "secret-node", "10.0.0.2", "credential-looking-provider-response"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metrics exposed sensitive or unbounded value %q:\n%s", forbidden, text)
		}
	}
}

func TestCollectorReportsListFailureWithoutInventingState(t *testing.T) {
	collector := NewCollector(failingReader{})
	registry := registerCollector(t, collector)
	text := gatherText(t, registry)
	if !strings.Contains(text, `waycloak_metrics_collection_success{source="pods"} 0`) || strings.Contains(text, "waycloak_enrolled_pods") {
		t.Fatalf("failed collection was not explicit:\n%s", text)
	}
}

func TestEnrolledPodStatesAreMutuallyExclusiveAndUIDBound(t *testing.T) {
	deleting := metav1.NewTime(time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC))
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "ordinary", UID: "ordinary"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "waiting", UID: "waiting", Labels: map[string]string{enrollment.RouteLabel: "route"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "absent", UID: "absent", Labels: map[string]string{enrollment.RouteLabel: "route"}}, Spec: corev1.PodSpec{NodeName: "node"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "denied", UID: "denied", Labels: map[string]string{enrollment.RouteLabel: "route"}}, Spec: corev1.PodSpec{NodeName: "node"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ready", UID: "ready", Labels: map[string]string{enrollment.RouteLabel: "route"}}, Spec: corev1.PodSpec{NodeName: "node"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "terminating", UID: "terminating", Labels: map[string]string{enrollment.RouteLabel: "route"}, DeletionTimestamp: &deleting}, Spec: corev1.PodSpec{NodeName: "node"}},
	}
	bindings := []wayv1.VPNWorkloadBinding{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "denied", Generation: 1},
			Spec:       wayv1.VPNWorkloadBindingSpec{PodRef: wayv1.LocalUIDReference{UID: "denied"}},
			Status:     wayv1.VPNWorkloadBindingStatus{Conditions: wayv1.BindingConditions{{Type: wayv1.ConditionReady, Status: metav1.ConditionFalse, Reason: wayv1.ReasonNotReady, ObservedGeneration: 1}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "ready", Generation: 2},
			Spec:       wayv1.VPNWorkloadBindingSpec{PodRef: wayv1.LocalUIDReference{UID: "ready"}},
			Status:     wayv1.VPNWorkloadBindingStatus{Conditions: wayv1.BindingConditions{{Type: wayv1.ConditionReady, Status: metav1.ConditionTrue, Reason: wayv1.ReasonReady, ObservedGeneration: 2}}},
		},
	}

	got := enrolledPodStates(pods, bindings)
	for _, state := range []string{"awaiting_capable_node", "binding_absent", "fail_closed", "ready", "terminating"} {
		if got[state] != 1 {
			t.Fatalf("state %q = %v, want 1; all states = %#v", state, got[state], got)
		}
	}
}

func TestAllocationStatesAreBounded(t *testing.T) {
	leases := []coordinationv1.Lease{
		{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{waybinding.ReservationStateAnnotation: waybinding.ReservationStateActive}}},
		{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{waybinding.ReservationStateAnnotation: waybinding.ReservationStateQuarantined}}},
		{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{waybinding.ReservationStateAnnotation: "future-state"}}},
		{},
	}
	got := allocationStates(leases)
	if got["active"] != 1 || got["quarantined"] != 1 || got["invalid"] != 2 || len(got) != 3 {
		t.Fatalf("allocation states = %#v", got)
	}
}

func TestConditionProjectionMakesMissingAndStaleStateExplicit(t *testing.T) {
	counts := map[conditionKey]float64{}
	addConditions(counts, "resource", 4, []string{wayv1.ConditionAccepted, wayv1.ConditionReady, wayv1.ConditionProgrammed}, []metav1.Condition{
		{Type: wayv1.ConditionAccepted, Status: metav1.ConditionTrue, Reason: wayv1.ReasonAccepted, ObservedGeneration: 4},
		{Type: wayv1.ConditionReady, Status: metav1.ConditionTrue, Reason: wayv1.ReasonReady, ObservedGeneration: 3},
	})

	wants := map[conditionKey]float64{
		{resource: "resource", condition: wayv1.ConditionAccepted, status: "True", reason: wayv1.ReasonAccepted, current: "true"}:    1,
		{resource: "resource", condition: wayv1.ConditionReady, status: "True", reason: wayv1.ReasonReady, current: "false"}:         1,
		{resource: "resource", condition: wayv1.ConditionProgrammed, status: "Unknown", reason: "ConditionAbsent", current: "false"}: 1,
	}
	if len(counts) != len(wants) {
		t.Fatalf("condition keys = %#v", counts)
	}
	for key, want := range wants {
		if counts[key] != want {
			t.Fatalf("condition %v = %v, want %v; all conditions = %#v", key, counts[key], want, counts)
		}
	}
}

func registerCollector(t *testing.T, collector prometheus.Collector) *prometheus.Registry {
	t.Helper()
	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}
	return registry
}

func gatherText(t *testing.T, registry *prometheus.Registry) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("metrics status = %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

type failingReader struct{}

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("API unavailable")
}
