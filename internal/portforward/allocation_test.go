// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
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

func TestProviderPortReservationRecoversAndQuarantinesReuse(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	kube := providerPortClient(t)
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: "gateway-uid"}}
	leaseA := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "apps", UID: "lease-a"}}
	allocator := ProviderPortAllocator{Client: kube, Now: func() time.Time { return now }}
	portA, err := allocator.Reserve(context.Background(), leaseA, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := allocator.Reserve(context.Background(), leaseA, gateway); err != nil || recovered != portA {
		t.Fatalf("reservation recovery = %d, %v; want %d", recovered, err, portA)
	}
	if err := allocator.Quarantine(context.Background(), leaseA, gateway, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	leaseB := collidingLease(t, gateway.UID, leaseA.UID, "b")
	portB, err := allocator.Reserve(context.Background(), leaseB, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if portB == portA {
		t.Fatal("quarantined provider port was reused")
	}

	now = now.Add(11 * time.Minute)
	leaseC := collidingLease(t, gateway.UID, leaseA.UID, "c")
	portC, err := allocator.Reserve(context.Background(), leaseC, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if portC != portA {
		t.Fatalf("expired quarantine retained port %d; allocation = %d", portA, portC)
	}
}

func TestProviderPortReservationIsIdempotentUnderConcurrentAllocation(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	kube := providerPortClient(t)
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: "gateway-uid"}}
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "lease", Namespace: "apps", UID: "lease-uid"}}
	allocator := ProviderPortAllocator{Client: kube, Now: func() time.Time { return now }}

	const workers = 8
	ports := make(chan uint16, workers)
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			port, err := allocator.Reserve(context.Background(), lease, gateway)
			ports <- port
			errorsFound <- err
		}()
	}
	group.Wait()
	close(ports)
	close(errorsFound)
	var expected uint16
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	for port := range ports {
		if expected == 0 {
			expected = port
		}
		if port != expected {
			t.Fatalf("one lease UID received ports %d and %d", expected, port)
		}
	}
	reservations := &coordinationv1.LeaseList{}
	if err := kube.List(context.Background(), reservations, client.InNamespace(gateway.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(reservations.Items) != 1 {
		t.Fatalf("concurrent reservation count = %d", len(reservations.Items))
	}
}

func TestProviderPortReservationRecoversExactIdentityWithoutUnsafeLabelValues(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	kube := providerPortClient(t)
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Namespace: "network", UID: types.UID(strings.Repeat("g", 128))}}
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", UID: types.UID(strings.Repeat("l", 128))}}
	allocator := ProviderPortAllocator{Client: kube, Now: func() time.Time { return now }}
	port, err := allocator.Reserve(context.Background(), lease, gateway)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := allocator.Recover(context.Background(), lease, gateway.Namespace)
	if err != nil || recovered.GatewayUID != gateway.UID || recovered.Port != port {
		t.Fatalf("recovered reservation = %#v, %v", recovered, err)
	}
	reservations := &coordinationv1.LeaseList{}
	if err := kube.List(context.Background(), reservations); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{providerGatewayUIDLabel, providerLeaseUIDLabel} {
		if value := reservations.Items[0].Labels[key]; len(value) > 63 || value == string(gateway.UID) || value == string(lease.UID) {
			t.Fatalf("unsafe identity label %s=%q", key, value)
		}
	}
}

func providerPortClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func collidingLease(t *testing.T, gatewayUID, targetUID types.UID, prefix string) *wayv1.PortForwardLease {
	t.Helper()
	want := providerStart(gatewayUID, targetUID)
	for index := 0; index < 200000; index++ {
		uid := types.UID(fmt.Sprintf("lease-%s-%d", prefix, index))
		if uid != targetUID && providerStart(gatewayUID, uid) == want {
			return &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("lease-%s", prefix), Namespace: "apps", UID: uid}}
		}
	}
	t.Fatal("could not find deterministic provider-port collision")
	return nil
}

func providerStart(gatewayUID, leaseUID types.UID) uint16 {
	count := uint32(ProviderPortLast-ProviderPortFirst) + 1
	seed := sha256.Sum256([]byte(string(gatewayUID) + "/" + string(leaseUID)))
	return ProviderPortFirst + uint16(binary.BigEndian.Uint32(seed[:4])%count)
}
