// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package reference

import (
	"context"
	"reflect"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestDependencyMapperCoversRouteAndLeaseConsentInputs(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	gateway := &wayv1.VPNGateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gateways", Name: "exit"},
		Spec:       wayv1.VPNGatewaySpec{GatewayClassName: "standard"},
	}
	route := &wayv1.VPNEgressRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "workloads", Name: "route"},
		Spec: wayv1.VPNEgressRouteSpec{ParentRefs: []wayv1.GatewayParentReference{{
			Group: wayv1.GroupName, Kind: "VPNGateway", Namespace: "gateways", Name: "exit",
		}}},
	}
	lease := &wayv1.PortForwardLease{
		ObjectMeta: metav1.ObjectMeta{Namespace: "workloads", Name: "lease"},
		Spec: wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedObjectReference{
			Namespace: "gateways", Name: "exit",
		}},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gateway, route, lease).
		WithIndex(&wayv1.VPNEgressRoute{}, RouteParentIndex, RouteParentIndexValues).
		WithIndex(&wayv1.VPNGateway{}, GatewayClassIndex, GatewayClassIndexValues).
		WithIndex(&wayv1.PortForwardLease{}, LeaseGatewayIndex, LeaseGatewayIndexValues).
		Build()
	mapper := DependencyMapper{Client: reader}
	ctx := context.Background()

	wantRoute := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "workloads", Name: "route"}}}
	wantLease := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "workloads", Name: "lease"}}}
	class := &wayv1.VPNGatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "standard"}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workloads"}}
	checks := []struct {
		name string
		got  []reconcile.Request
		want []reconcile.Request
	}{
		{name: "route gateway", got: mapper.RoutesForGateway(ctx, gateway), want: wantRoute},
		{name: "lease gateway", got: mapper.LeasesForGateway(ctx, gateway), want: wantLease},
		{name: "route class", got: mapper.RoutesForGatewayClass(ctx, class), want: wantRoute},
		{name: "lease class", got: mapper.LeasesForGatewayClass(ctx, class), want: wantLease},
		{name: "route namespace", got: mapper.RoutesForNamespace(ctx, namespace), want: wantRoute},
		{name: "lease namespace", got: mapper.LeasesForNamespace(ctx, namespace), want: wantLease},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if !reflect.DeepEqual(check.got, check.want) {
				t.Fatalf("requests = %#v, want %#v", check.got, check.want)
			}
		})
	}
}

func TestDependencyIndexValuesUseExactObjectIdentity(t *testing.T) {
	t.Parallel()
	route := &wayv1.VPNEgressRoute{Spec: wayv1.VPNEgressRouteSpec{ParentRefs: []wayv1.GatewayParentReference{{Namespace: "gateways", Name: "exit"}}}}
	lease := &wayv1.PortForwardLease{Spec: wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedObjectReference{Namespace: "gateways", Name: "exit"}}}
	gateway := &wayv1.VPNGateway{Spec: wayv1.VPNGatewaySpec{GatewayClassName: "standard"}}
	checks := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "route", got: RouteParentIndexValues(route), want: []string{"gateways/exit"}},
		{name: "lease", got: LeaseGatewayIndexValues(lease), want: []string{"gateways/exit"}},
		{name: "class", got: GatewayClassIndexValues(gateway), want: []string{"standard"}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if !reflect.DeepEqual(check.got, check.want) {
				t.Fatalf("index values = %v, want %v", check.got, check.want)
			}
		})
	}
	if got := RouteParentIndexValues(&wayv1.VPNEgressRoute{}); got != nil {
		t.Fatalf("route without its single parent indexed as %v", got)
	}
}
