// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package binding implements the replacement API's durable, UID-bound
// allocation protocol. Kubernetes Lease objects are used as atomic address
// reservations; they are not the CNI handshake and never contain credentials
// or data-plane configuration.
package binding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ReservationManagedByLabel = "networking.waycloak.io/managed-by"
	ReservationGatewayLabel   = "networking.waycloak.io/gateway-identity"
	ReservationManagedByValue = "binding-controller"

	ReservationGatewayUIDAnnotation = "networking.waycloak.io/gateway-uid"
	ReservationPodUIDAnnotation     = "networking.waycloak.io/pod-uid"
	ReservationAddressAnnotation    = "networking.waycloak.io/address"
	ReservationStateAnnotation      = "networking.waycloak.io/reservation-state"
	ReservationStateActive          = "Active"
	ReservationStateQuarantined     = "Quarantined"
)

var (
	ErrPoolExhausted       = errors.New("gateway workload address pool is exhausted")
	ErrUnsafePool          = errors.New("gateway workload address pool is not safely allocatable")
	ErrReservationConflict = errors.New("address reservation belongs to another allocation")
	ErrReservationDeleting = errors.New("address reservation deletion is still in progress")
	ErrIdentityQuarantined = errors.New("allocation identity has a quarantined address")
)

type Reservation struct {
	Lease     *coordinationv1.Lease
	Identity  string
	Address   netip.Prefix
	Recovered bool
}

type Allocator struct {
	Client client.Client
	Reader client.Reader
	Now    func() time.Time
}

// Reserve atomically claims one address for an exact gateway and Pod UID. A
// deterministic probe order makes crash recovery stable, while Lease CREATE is
// the collision boundary for concurrent controller replicas.
func (a Allocator) Reserve(ctx context.Context, gateway *wayv1.VPNGateway, podUID types.UID, pool netip.Prefix) (Reservation, error) {
	if a.Client == nil || gateway == nil || gateway.UID == "" || podUID == "" {
		return Reservation{}, errors.New("client and exact gateway and Pod UIDs are required")
	}
	pool = pool.Masked()
	if !pool.IsValid() || !pool.Addr().Is4() || pool.Bits() < 16 || pool.Bits() > 29 {
		return Reservation{}, fmt.Errorf("%w: require an IPv4 /16 through /29 pool", ErrUnsafePool)
	}

	identity := allocationIdentity(gateway.UID, podUID)
	reservations := &coordinationv1.LeaseList{}
	if err := a.reader().List(ctx, reservations, client.InNamespace(gateway.Namespace), client.MatchingLabels{
		ReservationManagedByLabel: ReservationManagedByValue,
		ReservationGatewayLabel:   shortHash(string(gateway.UID)),
	}); err != nil {
		return Reservation{}, fmt.Errorf("inspect durable address reservations: %w", err)
	}
	for i := range reservations.Items {
		lease := &reservations.Items[i]
		if lease.Annotations[ReservationGatewayUIDAnnotation] == string(gateway.UID) &&
			lease.Annotations[ReservationPodUIDAnnotation] == string(podUID) &&
			lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == identity &&
			lease.Annotations[ReservationStateAnnotation] == ReservationStateQuarantined {
			return Reservation{}, ErrIdentityQuarantined
		}
	}
	hosts := uint32(1) << uint32(32-pool.Bits())
	usable := hosts - 3 // network, gateway (.1), and broadcast are reserved.
	seed := sha256.Sum256([]byte(string(podUID)))
	start := uint32(seed[0])<<24 | uint32(seed[1])<<16 | uint32(seed[2])<<8 | uint32(seed[3])
	start %= usable
	base := pool.Addr().As4()
	baseValue := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])

	for attempt := uint32(0); attempt < usable; attempt++ {
		offset := uint32(2) + (start+attempt)%usable
		value := baseValue + offset
		address := netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
		prefix := netip.PrefixFrom(address, 32)
		lease := a.desiredLease(gateway, podUID, identity, prefix)
		err := a.Client.Create(ctx, lease, client.FieldOwner(wayv1.FieldManagerBindingController))
		if err == nil {
			return Reservation{Lease: lease, Identity: identity, Address: prefix}, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return Reservation{}, fmt.Errorf("create durable address reservation: %w", err)
		}
		existing := &coordinationv1.Lease{}
		if err := a.reader().Get(ctx, client.ObjectKeyFromObject(lease), existing); err != nil {
			if apierrors.IsNotFound(err) {
				if attempt > 0 {
					attempt--
				}
				continue
			}
			return Reservation{}, fmt.Errorf("read conflicting address reservation: %w", err)
		}
		if reservationMatches(existing, gateway.UID, podUID, identity, prefix) {
			return Reservation{Lease: existing, Identity: identity, Address: prefix, Recovered: true}, nil
		}
	}
	return Reservation{}, ErrPoolExhausted
}

func (a Allocator) Quarantine(ctx context.Context, binding *wayv1.VPNWorkloadBinding) error {
	if a.Client == nil || binding == nil {
		return errors.New("client and binding are required")
	}
	address, err := netip.ParsePrefix(binding.Spec.Allocation.Address)
	if err != nil {
		return fmt.Errorf("parse binding address for quarantine: %w", err)
	}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{
		Name: string(binding.Spec.GatewayRef.Name), Namespace: string(binding.Spec.GatewayRef.Namespace), UID: types.UID(binding.Spec.GatewayRef.UID),
	}}
	desired := a.desiredLease(gateway, types.UID(binding.Spec.PodRef.UID), binding.Spec.Allocation.Identity, address)
	desired.Annotations[ReservationStateAnnotation] = ReservationStateQuarantined
	if err := a.Client.Create(ctx, desired, client.FieldOwner(wayv1.FieldManagerBindingController)); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create durable quarantine: %w", err)
	}
	current := &coordinationv1.Lease{}
	if err := a.reader().Get(ctx, client.ObjectKeyFromObject(desired), current); err != nil {
		return fmt.Errorf("read address reservation for quarantine: %w", err)
	}
	if !current.DeletionTimestamp.IsZero() {
		return ErrReservationDeleting
	}
	if current.Annotations[ReservationStateAnnotation] == ReservationStateQuarantined {
		if quarantineMatches(current, gateway.UID, types.UID(binding.Spec.PodRef.UID), binding.Spec.Allocation.Identity, address) {
			return nil
		}
		return ErrReservationConflict
	}
	if !reservationMatches(current, gateway.UID, types.UID(binding.Spec.PodRef.UID), binding.Spec.Allocation.Identity, address) {
		return ErrReservationConflict
	}
	copy := current.DeepCopy()
	if copy.Annotations == nil {
		copy.Annotations = map[string]string{}
	}
	copy.Annotations[ReservationStateAnnotation] = ReservationStateQuarantined
	return a.Client.Patch(ctx, copy, client.MergeFrom(current), client.FieldOwner(wayv1.FieldManagerBindingController))
}

func (a Allocator) Release(ctx context.Context, binding *wayv1.VPNWorkloadBinding) error {
	if a.Client == nil || binding == nil {
		return errors.New("client and binding are required")
	}
	key, err := ReservationForBinding(binding)
	if err != nil {
		return err
	}
	current := &coordinationv1.Lease{}
	if err := a.reader().Get(ctx, client.ObjectKeyFromObject(key), current); err != nil {
		return client.IgnoreNotFound(err)
	}
	address, err := netip.ParsePrefix(binding.Spec.Allocation.Address)
	if err != nil {
		return err
	}
	if !reservationMatches(current, types.UID(binding.Spec.GatewayRef.UID), types.UID(binding.Spec.PodRef.UID), binding.Spec.Allocation.Identity, address) {
		return ErrReservationConflict
	}
	return client.IgnoreNotFound(a.Client.Delete(ctx, current))
}

// Ensure restores or validates the exact reservation recorded in a binding.
// It never selects a replacement address, because doing so behind the immutable
// binding identity would make cached desired state ambiguous.
func (a Allocator) Ensure(ctx context.Context, gateway *wayv1.VPNGateway, binding *wayv1.VPNWorkloadBinding) (*coordinationv1.Lease, error) {
	if a.Client == nil || gateway == nil || binding == nil {
		return nil, errors.New("client, gateway, and binding are required")
	}
	address, err := netip.ParsePrefix(binding.Spec.Allocation.Address)
	if err != nil {
		return nil, fmt.Errorf("parse binding address: %w", err)
	}
	lease := a.desiredLease(gateway, types.UID(binding.Spec.PodRef.UID), binding.Spec.Allocation.Identity, address)
	if err := a.Client.Create(ctx, lease, client.FieldOwner(wayv1.FieldManagerBindingController)); err == nil {
		return lease, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("restore durable address reservation: %w", err)
	}
	existing := &coordinationv1.Lease{}
	if err := a.reader().Get(ctx, client.ObjectKeyFromObject(lease), existing); err != nil {
		return nil, fmt.Errorf("read durable address reservation: %w", err)
	}
	if !reservationMatches(existing, gateway.UID, types.UID(binding.Spec.PodRef.UID), binding.Spec.Allocation.Identity, address) {
		return nil, ErrReservationConflict
	}
	return existing, nil
}

func (a Allocator) desiredLease(gateway *wayv1.VPNGateway, podUID types.UID, identity string, address netip.Prefix) *coordinationv1.Lease {
	now := metav1.NewMicroTime(a.now())
	holder := identity
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reservationName(gateway.UID, address),
			Namespace: gateway.Namespace,
			Labels: map[string]string{
				ReservationManagedByLabel: ReservationManagedByValue,
				ReservationGatewayLabel:   shortHash(string(gateway.UID)),
			},
			Annotations: map[string]string{
				ReservationGatewayUIDAnnotation: string(gateway.UID),
				ReservationPodUIDAnnotation:     string(podUID),
				ReservationAddressAnnotation:    address.String(),
				ReservationStateAnnotation:      ReservationStateActive,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: wayv1.GroupVersion.String(), Kind: "VPNGateway",
				Name: gateway.Name, UID: gateway.UID,
			}},
		},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, AcquireTime: &now},
	}
}

func reservationMatches(lease *coordinationv1.Lease, gatewayUID, podUID types.UID, identity string, address netip.Prefix) bool {
	return lease != nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == identity &&
		lease.Annotations[ReservationGatewayUIDAnnotation] == string(gatewayUID) &&
		lease.Annotations[ReservationPodUIDAnnotation] == string(podUID) &&
		lease.Annotations[ReservationAddressAnnotation] == address.String() &&
		lease.Annotations[ReservationStateAnnotation] == ReservationStateActive
}

func quarantineMatches(lease *coordinationv1.Lease, gatewayUID, podUID types.UID, identity string, address netip.Prefix) bool {
	return lease != nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == identity &&
		lease.Annotations[ReservationGatewayUIDAnnotation] == string(gatewayUID) &&
		lease.Annotations[ReservationPodUIDAnnotation] == string(podUID) &&
		lease.Annotations[ReservationAddressAnnotation] == address.String() &&
		lease.Annotations[ReservationStateAnnotation] == ReservationStateQuarantined
}

func allocationIdentity(gatewayUID, podUID types.UID) string {
	sum := sha256.Sum256([]byte("waycloak-allocation-v1\x00" + string(gatewayUID) + "\x00" + string(podUID)))
	return "alloc-" + hex.EncodeToString(sum[:16])
}

func BindingName(podUID types.UID) string {
	return "pod-" + shortHash(string(podUID))
}

func reservationName(gatewayUID types.UID, address netip.Prefix) string {
	return "waycloak-address-" + shortHash(string(gatewayUID)+"\x00"+address.String())
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:10])
}

func (a Allocator) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func (a Allocator) reader() client.Reader {
	if a.Reader != nil {
		return a.Reader
	}
	return a.Client
}

// ReservationForBinding reconstructs the immutable reservation key without
// trusting mutable labels or a list order.
func ReservationForBinding(binding *wayv1.VPNWorkloadBinding) (*coordinationv1.Lease, error) {
	if binding == nil {
		return nil, errors.New("binding is required")
	}
	address, err := netip.ParsePrefix(binding.Spec.Allocation.Address)
	if err != nil {
		return nil, fmt.Errorf("parse binding allocation address: %w", err)
	}
	return &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name:      reservationName(types.UID(binding.Spec.GatewayRef.UID), address),
		Namespace: string(binding.Spec.GatewayRef.Namespace),
	}}, nil
}

func ParseOverlayCIDR(gateway *wayv1.VPNGateway) (netip.Prefix, error) {
	if gateway == nil {
		return netip.Prefix{}, errors.New("gateway is required")
	}
	for _, address := range gateway.Status.Addresses {
		if address.Type != wayv1.GatewayAddressOverlayCIDR {
			continue
		}
		prefix, err := netip.ParsePrefix(address.Value)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("parse observed gateway overlay CIDR: %w", err)
		}
		return prefix.Masked(), nil
	}
	return netip.Prefix{}, errors.New("gateway has no observed overlay CIDR")
}
