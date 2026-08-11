// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"reflect"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/reference"
	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestRouteStatusFailsClosedAcrossParentStates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		route   *wayv1.VPNEgressRoute
		gateway *wayv1.VPNGateway
		want    map[string]conditionState
	}{
		{
			name:  "missing same namespace parent",
			route: testRoute("apps", "apps", nil),
			want: map[string]conditionState{
				wayv1.ConditionAccepted:     {status: metav1.ConditionTrue, reason: "Accepted"},
				wayv1.ConditionResolvedRefs: {status: metav1.ConditionFalse, reason: "RefNotFound"},
				wayv1.ConditionProgrammed:   {status: metav1.ConditionFalse, reason: "Pending"},
				wayv1.ConditionReady:        {status: metav1.ConditionFalse, reason: "NotReady"},
			},
		},
		{
			name:    "cross namespace denied before lookup",
			route:   testRoute("apps", "gateways", nil),
			gateway: readyGateway("gateways"),
			want: map[string]conditionState{
				wayv1.ConditionAccepted:     {status: metav1.ConditionTrue, reason: "Accepted"},
				wayv1.ConditionResolvedRefs: {status: metav1.ConditionFalse, reason: "RefNotPermitted"},
				wayv1.ConditionProgrammed:   {status: metav1.ConditionFalse, reason: "Pending"},
				wayv1.ConditionReady:        {status: metav1.ConditionFalse, reason: "NotReady"},
			},
		},
		{
			name:    "authorized ready parent",
			route:   testRoute("apps", "gateways", nil),
			gateway: allowAllRoutes(readyGateway("gateways")),
			want: map[string]conditionState{
				wayv1.ConditionAccepted:     {status: metav1.ConditionTrue, reason: "Accepted"},
				wayv1.ConditionResolvedRefs: {status: metav1.ConditionTrue, reason: "ResolvedRefs"},
				wayv1.ConditionProgrammed:   {status: metav1.ConditionTrue, reason: "Programmed"},
				wayv1.ConditionReady:        {status: metav1.ConditionTrue, reason: "Ready"},
			},
		},
		{
			name:    "unsupported required feature",
			route:   testRoute("apps", "apps", []wayv1.FeatureName{wayv1.FeaturePortForwardSingleActive}),
			gateway: readyGateway("apps"),
			want: map[string]conditionState{
				wayv1.ConditionAccepted:     {status: metav1.ConditionFalse, reason: "UnsupportedFeature"},
				wayv1.ConditionResolvedRefs: {status: metav1.ConditionTrue, reason: "ResolvedRefs"},
				wayv1.ConditionProgrammed:   {status: metav1.ConditionFalse, reason: "Pending"},
				wayv1.ConditionReady:        {status: metav1.ConditionFalse, reason: "NotReady"},
			},
		},
		{
			name:  "stale gateway observation is unknown",
			route: testRoute("apps", "apps", nil),
			gateway: func() *wayv1.VPNGateway {
				gateway := readyGateway("apps")
				gateway.Status.ObservedGeneration--
				return gateway
			}(),
			want: map[string]conditionState{
				wayv1.ConditionAccepted:     {status: metav1.ConditionUnknown, reason: "ObservationUnavailable"},
				wayv1.ConditionResolvedRefs: {status: metav1.ConditionTrue, reason: "ResolvedRefs"},
				wayv1.ConditionProgrammed:   {status: metav1.ConditionUnknown, reason: "ObservationUnavailable"},
				wayv1.ConditionReady:        {status: metav1.ConditionUnknown, reason: "ObservationUnavailable"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			if err := wayv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			objects := []client.Object{}
			if tt.gateway != nil {
				objects = append(objects, tt.gateway)
			}
			r := &VPNEgressRouteReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), Now: func() time.Time { return now }}
			status := r.desiredStatus(context.Background(), tt.route)
			if status.ObservedGeneration != tt.route.Generation {
				t.Fatalf("observedGeneration = %d", status.ObservedGeneration)
			}
			if len(status.Parents) != 1 || status.Parents[0].ControllerName != RouteControllerName {
				t.Fatalf("parent status identity = %#v", status.Parents)
			}
			for conditionType, want := range tt.want {
				got := apiMeta.FindStatusCondition(status.Conditions, conditionType)
				if got == nil || got.Status != want.status || got.Reason != want.reason {
					t.Errorf("%s = %#v, want status=%s reason=%s", conditionType, got, want.status, want.reason)
				}
				if got != nil && !got.LastTransitionTime.Time.Equal(now) {
					t.Errorf("%s transition = %s", conditionType, got.LastTransitionTime)
				}
			}
		})
	}
}

func TestRouteStatusDoesNotRefreshTransitionTime(t *testing.T) {
	t.Parallel()
	oldTime := metav1.NewTime(time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC))
	route := testRoute("apps", "apps", nil)
	route.Status.Conditions = wayv1.Conditions{{Type: wayv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: route.Generation, LastTransitionTime: oldTime}}
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &VPNEgressRouteReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyGateway("apps")).Build(), Now: func() time.Time { return oldTime.Add(24 * time.Hour) }}
	status := r.desiredStatus(context.Background(), route)
	ready := apiMeta.FindStatusCondition(status.Conditions, wayv1.ConditionReady)
	if ready == nil || !ready.LastTransitionTime.Equal(&oldTime) {
		t.Fatalf("Ready transition refreshed: %#v", ready)
	}
}

func TestRouteAuthorizationUsesFreshReaderSnapshot(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cachedGateway := allowAllRoutes(readyGateway("gateways"))
	freshGateway := readyGateway("gateways")
	cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cachedGateway).Build()
	fresh := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshGateway).Build()
	route := testRoute("apps", "gateways", nil)
	r := &VPNEgressRouteReconciler{Client: cached, APIReader: fresh}
	status := r.desiredStatus(context.Background(), route)
	resolved := apiMeta.FindStatusCondition(status.Conditions, wayv1.ConditionResolvedRefs)
	ready := apiMeta.FindStatusCondition(status.Conditions, wayv1.ConditionReady)
	if resolved == nil || resolved.Status != metav1.ConditionFalse || resolved.Reason != "RefNotPermitted" {
		t.Fatalf("fresh consent decision = %#v", resolved)
	}
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("revoked consent did not withdraw readiness: %#v", ready)
	}
}

func TestRouteDependencyMappings(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	gatewayA := readyGateway("gateways")
	gatewayA.Name = "a"
	gatewayA.Spec.GatewayClassName = "class.example.io"
	gatewayB := readyGateway("gateways")
	gatewayB.Name = "b"
	gatewayB.Spec.GatewayClassName = "class.example.io"
	routeA := testRoute("apps", "gateways", nil)
	routeA.Name = "a"
	routeA.Spec.ParentRefs[0].Name = "a"
	routeB := testRoute("apps", "gateways", nil)
	routeB.Name = "b"
	routeB.Spec.ParentRefs[0].Name = "b"
	routeOther := testRoute("other", "gateways", nil)
	routeOther.Name = "other"
	routeOther.Spec.ParentRefs[0].Name = "a"
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gatewayA, gatewayB, routeA, routeB, routeOther).
		WithIndex(&wayv1.VPNEgressRoute{}, reference.RouteParentIndex, reference.RouteParentIndexValues).
		WithIndex(&wayv1.VPNGateway{}, reference.GatewayClassIndex, reference.GatewayClassIndexValues).
		Build()
	r := &VPNEgressRouteReconciler{Client: client}
	wantNamespace := []reconcile.Request{{NamespacedName: clientObjectKey("apps", "a")}, {NamespacedName: clientObjectKey("apps", "b")}}
	if got := r.routesForNamespace(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}}); !reflect.DeepEqual(got, wantNamespace) {
		t.Fatalf("namespace mapping = %#v, want %#v", got, wantNamespace)
	}
	wantClass := []reconcile.Request{{NamespacedName: clientObjectKey("apps", "a")}, {NamespacedName: clientObjectKey("apps", "b")}, {NamespacedName: clientObjectKey("other", "other")}}
	if got := r.routesForGatewayClass(context.Background(), &wayv1.VPNGatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "class.example.io"}}); !reflect.DeepEqual(got, wantClass) {
		t.Fatalf("class mapping = %#v, want %#v", got, wantClass)
	}
}

func clientObjectKey(namespace, name string) client.ObjectKey {
	return client.ObjectKey{Namespace: namespace, Name: name}
}

func testRoute(namespace, parentNamespace string, features []wayv1.FeatureName) *wayv1.VPNEgressRoute {
	return &wayv1.VPNEgressRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "private", Generation: 2},
		Spec:       wayv1.VPNEgressRouteSpec{ParentRefs: []wayv1.GatewayParentReference{{Group: wayv1.GroupName, Kind: "VPNGateway", Namespace: wayv1.NamespaceName(parentNamespace), Name: "gateway"}}, RequiredFeatures: features},
	}
}

func allowAllRoutes(gateway *wayv1.VPNGateway) *wayv1.VPNGateway {
	gateway.Spec.AllowedRoutes.Namespaces.From = wayv1.RouteNamespaceAll
	return gateway
}

func readyGateway(namespace string) *wayv1.VPNGateway {
	generation := int64(4)
	conditions := wayv1.GatewayConditions{}
	for _, conditionType := range []string{wayv1.ConditionAccepted, wayv1.ConditionProgrammed, wayv1.ConditionReady} {
		conditions = append(conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: conditionType, ObservedGeneration: generation})
	}
	return &wayv1.VPNGateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "gateway", Generation: generation},
		Status:     wayv1.VPNGatewayStatus{ObservedGeneration: generation, SupportedFeatures: wayv1.BaselineFeatures(), Conditions: conditions},
	}
}
