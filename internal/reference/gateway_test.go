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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGatewayAuthorizerConsentAndPrivacy(t *testing.T) {
	t.Parallel()
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"networking.waycloak.io/gateway-access": "allowed"}}
	tests := []struct {
		name            string
		sourceNamespace string
		gateway         *wayv1.VPNGateway
		namespace       *corev1.Namespace
		wantPermitted   bool
		wantGateway     bool
	}{
		{name: "same namespace missing reports authorized absence", sourceNamespace: "gateways", wantPermitted: true},
		{name: "cross namespace missing hides absence", sourceNamespace: "apps"},
		{name: "default Same denies cross namespace", sourceNamespace: "apps", gateway: gatewayWithPolicy(wayv1.RouteNamespaceSame, nil)},
		{name: "All permits cross namespace", sourceNamespace: "apps", gateway: gatewayWithPolicy(wayv1.RouteNamespaceAll, nil), wantPermitted: true, wantGateway: true},
		{name: "Selector permits operator label", sourceNamespace: "apps", gateway: gatewayWithPolicy(wayv1.RouteNamespaceSelector, selector), namespace: testNamespace("apps", map[string]string{"networking.waycloak.io/gateway-access": "allowed"}), wantPermitted: true, wantGateway: true},
		{name: "Selector denies missing label", sourceNamespace: "apps", gateway: gatewayWithPolicy(wayv1.RouteNamespaceSelector, selector), namespace: testNamespace("apps", nil)},
		{name: "Selector denies hostile value", sourceNamespace: "apps", gateway: gatewayWithPolicy(wayv1.RouteNamespaceSelector, selector), namespace: testNamespace("apps", map[string]string{"networking.waycloak.io/gateway-access": "denied"})},
		{name: "Selector without selector denies", sourceNamespace: "apps", gateway: gatewayWithPolicy(wayv1.RouteNamespaceSelector, nil)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scheme := referenceScheme(t)
			objects := []client.Object{}
			if tt.gateway != nil {
				objects = append(objects, tt.gateway)
			}
			if tt.namespace != nil {
				objects = append(objects, tt.namespace)
			}
			reader := &recordingReader{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()}
			resolution, err := (GatewayAuthorizer{Reader: reader}).ResolveGateway(context.Background(), tt.sourceNamespace, wayv1.NamespacedObjectReference{Namespace: "gateways", Name: "private"})
			if err != nil {
				t.Fatal(err)
			}
			if resolution.Permitted != tt.wantPermitted || (resolution.Gateway != nil) != tt.wantGateway {
				t.Fatalf("resolution = %#v, want permitted=%t gateway=%t", resolution, tt.wantPermitted, tt.wantGateway)
			}
			for _, kind := range reader.kinds {
				if kind != "VPNGateway" && kind != "Namespace" {
					t.Fatalf("authorization observed unauthorized %s", kind)
				}
			}
		})
	}
}

func TestCrossNamespaceMissingAndDeniedAreIndistinguishable(t *testing.T) {
	t.Parallel()
	scheme := referenceScheme(t)
	missing := GatewayAuthorizer{Reader: fake.NewClientBuilder().WithScheme(scheme).Build()}
	denied := GatewayAuthorizer{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(gatewayWithPolicy(wayv1.RouteNamespaceSame, nil)).Build()}
	ref := wayv1.NamespacedObjectReference{Namespace: "gateways", Name: "private"}
	missingResult, err := missing.ResolveGateway(context.Background(), "tenant", ref)
	if err != nil {
		t.Fatal(err)
	}
	deniedResult, err := denied.ResolveGateway(context.Background(), "tenant", ref)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(missingResult, deniedResult) {
		t.Fatalf("missing = %#v, denied = %#v", missingResult, deniedResult)
	}
}

type recordingReader struct {
	client.Reader
	kinds []string
}

func (r *recordingReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	r.kinds = append(r.kinds, reflect.TypeOf(object).Elem().Name())
	return r.Reader.Get(ctx, key, object, options...)
}

func gatewayWithPolicy(from wayv1.RouteNamespaceFrom, selector *metav1.LabelSelector) *wayv1.VPNGateway {
	return &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "gateways"}, Spec: wayv1.VPNGatewaySpec{AllowedRoutes: wayv1.AllowedRoutes{Namespaces: wayv1.RouteNamespaces{From: from, Selector: selector}}}}
}

func testNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func referenceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
