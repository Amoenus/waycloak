// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package binding

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReserveIsPersistentCollisionSafeAndQuarantinesIdentity(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: types.UID("gateway-uid")}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway).Build()
	allocator := Allocator{Client: client, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	pool := netip.MustParsePrefix("192.0.2.0/29")

	first, err := allocator.Reserve(context.Background(), gateway, types.UID("pod-a"), pool)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := allocator.Reserve(context.Background(), gateway, types.UID("pod-a"), pool)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Recovered || recovered.Identity != first.Identity || recovered.Address != first.Address {
		t.Fatalf("recovered reservation = %#v, want exact %#v", recovered, first)
	}
	second, err := allocator.Reserve(context.Background(), gateway, types.UID("pod-b"), pool)
	if err != nil {
		t.Fatal(err)
	}
	if second.Address == first.Address || second.Identity == first.Identity {
		t.Fatal("distinct Pod UIDs collided")
	}
	binding := &wayv1.VPNWorkloadBinding{Spec: wayv1.VPNWorkloadBindingSpec{
		PodRef: wayv1.LocalUIDReference{UID: "pod-a"}, GatewayRef: wayv1.NamespacedUIDReference{Namespace: "network", Name: "gateway", UID: "gateway-uid"},
		Allocation: wayv1.WorkloadAllocation{Identity: first.Identity, Address: first.Address.String()},
	}}
	// Even if the active reservation was lost, bounded cleanup must recreate a
	// durable quarantine before the binding finalizer can be released.
	if err := client.Delete(context.Background(), first.Lease); err != nil {
		t.Fatal(err)
	}
	if err := allocator.Quarantine(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.Reserve(context.Background(), gateway, types.UID("pod-a"), pool); !errors.Is(err, ErrIdentityQuarantined) {
		t.Fatalf("reserve after quarantine error = %v, want %v", err, ErrIdentityQuarantined)
	}
}

func TestReserveReportsExhaustionWithoutListOrderIdentity(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = wayv1.AddToScheme(scheme)
	_ = coordinationv1.AddToScheme(scheme)
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: types.UID("gateway-uid")}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway).Build()
	allocator := Allocator{Client: client}
	pool := netip.MustParsePrefix("198.51.100.0/29")
	for _, uid := range []types.UID{"a", "b", "c", "d", "e"} {
		if _, err := allocator.Reserve(context.Background(), gateway, uid, pool); err != nil {
			t.Fatalf("reserve %s: %v", uid, err)
		}
	}
	if _, err := allocator.Reserve(context.Background(), gateway, types.UID("f"), pool); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("sixth reservation error = %v, want exhaustion", err)
	}
	leases := &coordinationv1.LeaseList{}
	if err := client.List(context.Background(), leases); err != nil {
		t.Fatal(err)
	}
	if len(leases.Items) != 5 {
		t.Fatalf("reservation count = %d, want 5", len(leases.Items))
	}
}

func TestReserveUsesAuthoritativeReaderNotStaleClientCache(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = wayv1.AddToScheme(scheme)
	_ = coordinationv1.AddToScheme(scheme)
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: types.UID("gateway-uid")}}
	authoritative := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway).Build()
	first, err := (Allocator{Client: authoritative, Reader: authoritative}).Reserve(context.Background(), gateway, "pod-uid", netip.MustParsePrefix("203.0.113.0/29"))
	if err != nil {
		t.Fatal(err)
	}
	stale := staleReadClient{Client: authoritative}
	recovered, err := (Allocator{Client: stale, Reader: authoritative}).Reserve(context.Background(), gateway, "pod-uid", netip.MustParsePrefix("203.0.113.0/29"))
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Recovered || recovered.Address != first.Address {
		t.Fatalf("authoritative recovery = %#v, want %#v", recovered, first)
	}
}

func TestParseOverlayCIDRRequiresTypedObservedAddress(t *testing.T) {
	gateway := &wayv1.VPNGateway{Status: wayv1.VPNGatewayStatus{Addresses: []wayv1.GatewayAddress{{Type: wayv1.GatewayAddressOverlayCIDR, Value: "203.0.113.0/24"}}}}
	got, err := ParseOverlayCIDR(gateway)
	if err != nil {
		t.Fatal(err)
	}
	if got != netip.MustParsePrefix("203.0.113.0/24") {
		t.Fatalf("pool = %s", got)
	}
}

type staleReadClient struct{ client.Client }

func (staleReadClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("stale cached Get must not be used")
}

func (staleReadClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("stale cached List must not be used")
}
