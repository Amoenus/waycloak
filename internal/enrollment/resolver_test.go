// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package enrollment

import (
	"context"
	"errors"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResolverEnrollmentStates(t *testing.T) {
	t.Parallel()
	readyRoute := routeWithConditions("workloads", "private", "route-uid", 3, metav1.ConditionTrue)
	staleRoute := readyRoute.DeepCopy()
	staleRoute.Name = "stale"
	staleRoute.UID = "stale-uid"
	staleRoute.Status.ObservedGeneration = 2
	rejectedRoute := routeWithConditions("workloads", "rejected", "rejected-uid", 1, metav1.ConditionFalse)

	tests := []struct {
		name       string
		labels     map[string]string
		objects    []runtime.Object
		want       Resolution
		wantErrIs  error
		resolvedID types.UID
	}{
		{name: "unlabeled Pod is untouched", want: Resolution{PodUID: "pod-uid", Reason: ReasonUnenrolled}},
		{name: "missing route remains enrolled and closed", labels: map[string]string{RouteLabel: "missing"}, want: Resolution{PodUID: "pod-uid", RouteName: "missing", Enrolled: true, Reason: ReasonRouteNotFound}},
		{name: "malformed key remains enrolled and closed", labels: map[string]string{RouteLabel: "Not_DNS"}, want: Resolution{PodUID: "pod-uid", RouteName: "Not_DNS", Enrolled: true, Reason: ReasonInvalidEnrollment}},
		{name: "current positive route is ready", labels: map[string]string{RouteLabel: "private"}, objects: []runtime.Object{readyRoute}, want: Resolution{PodUID: "pod-uid", RouteName: "private", RouteUID: "route-uid", Enrolled: true, Ready: true, Reason: ReasonReady}},
		{name: "stale observation remains closed", labels: map[string]string{RouteLabel: "stale"}, objects: []runtime.Object{staleRoute}, want: Resolution{PodUID: "pod-uid", RouteName: "stale", RouteUID: "stale-uid", Enrolled: true, Reason: ReasonObservationUnavailable}},
		{name: "rejected route remains closed", labels: map[string]string{RouteLabel: "rejected"}, objects: []runtime.Object{rejectedRoute}, want: Resolution{PodUID: "pod-uid", RouteName: "rejected", RouteUID: "rejected-uid", Enrolled: true, Reason: "InvalidRef"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := wayv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "workloads", Name: "app", UID: "pod-uid", Labels: tt.labels}}
			objects := append([]runtime.Object{pod}, tt.objects...)
			resolver := Resolver{Reader: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()}
			got, err := resolver.Resolve(context.Background(), "workloads", "app", "pod-uid")
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("Resolve() error = %v, want %v", err, tt.wantErrIs)
			}
			if got != tt.want {
				t.Fatalf("Resolve() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolverRejectsPodNameReuse(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "workloads", Name: "app", UID: "replacement-uid", Labels: map[string]string{RouteLabel: "private"}}}
	resolver := Resolver{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()}
	got, err := resolver.Resolve(context.Background(), "workloads", "app", "deleted-uid")
	if !errors.Is(err, ErrPodUIDMismatch) {
		t.Fatalf("Resolve() error = %v, want ErrPodUIDMismatch", err)
	}
	if got.Enrolled {
		t.Fatalf("mismatched Pod identity must not resolve enrollment: %#v", got)
	}
}

func TestHasAlphaAnnotation(t *testing.T) {
	t.Parallel()
	for _, annotations := range []map[string]string{
		{"networking.waycloak.io/gateway": "old"},
		{"internal.networking.waycloak.io/allocation": "old"},
	} {
		if !HasAlphaAnnotation(annotations) {
			t.Fatalf("expected alpha annotation rejection for %#v", annotations)
		}
	}
	if HasAlphaAnnotation(map[string]string{"example.com/owner": "team"}) {
		t.Fatal("unrelated annotations must remain allowed")
	}
}

func routeWithConditions(namespace, name string, uid types.UID, generation int64, status metav1.ConditionStatus) *wayv1.VPNEgressRoute {
	conditions := make([]metav1.Condition, 0, 4)
	for _, conditionType := range []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady} {
		reason := conditionType
		if status != metav1.ConditionTrue {
			reason = "InvalidRef"
		}
		conditions = append(conditions, metav1.Condition{Type: conditionType, Status: status, Reason: reason, ObservedGeneration: generation})
	}
	return &wayv1.VPNEgressRoute{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: uid, Generation: generation}, Status: wayv1.VPNEgressRouteStatus{ObservedGeneration: generation, Conditions: conditions}}
}
