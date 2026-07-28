// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ProviderPortFirst = uint16(49152)
	ProviderPortLast  = uint16(65535)

	providerReservationLabel     = "networking.waycloak.io/provider-port-reservation"
	providerGatewayUIDLabel      = "networking.waycloak.io/provider-port-gateway-uid"
	providerLeaseUIDLabel        = "networking.waycloak.io/provider-port-lease-uid"
	providerGatewayUIDAnnotation = "networking.waycloak.io/provider-port-gateway-identity"
	providerLeaseUIDAnnotation   = "networking.waycloak.io/provider-port-lease-identity"
	providerPortLabel            = "networking.waycloak.io/provider-internal-port"
	providerStateLabel           = "networking.waycloak.io/provider-port-state"
	providerStateActive          = "active"
	providerStateQuarantine      = "quarantine"
)

var ErrProviderReservationNotFound = errors.New("provider port reservation was not found")

type ProviderPortAllocator struct {
	Client client.Client
	Now    func() time.Time
}

type ProviderReservationIdentity struct {
	GatewayUID       types.UID
	GatewayNamespace string
	Port             uint16
}

func (a ProviderPortAllocator) Recover(ctx context.Context, lease *wayv1.PortForwardLease, gatewayNamespace string) (ProviderReservationIdentity, error) {
	if a.Client == nil || lease == nil || lease.UID == "" || gatewayNamespace == "" {
		return ProviderReservationIdentity{}, errors.New("exact lease identity and gateway namespace are required")
	}
	reservations := &coordinationv1.LeaseList{}
	if err := a.Client.List(ctx, reservations, client.InNamespace(gatewayNamespace), client.MatchingLabels{
		providerReservationLabel: "true", providerLeaseUIDLabel: identityLabelValue(lease.UID),
	}); err != nil {
		return ProviderReservationIdentity{}, fmt.Errorf("recover provider port reservation: %w", err)
	}
	reservations.Items = slices.DeleteFunc(reservations.Items, func(reservation coordinationv1.Lease) bool {
		return reservation.Annotations[providerLeaseUIDAnnotation] != string(lease.UID)
	})
	if len(reservations.Items) != 1 {
		if len(reservations.Items) == 0 {
			return ProviderReservationIdentity{}, ErrProviderReservationNotFound
		}
		return ProviderReservationIdentity{}, fmt.Errorf("expected one exact provider reservation, found %d", len(reservations.Items))
	}
	reservation := &reservations.Items[0]
	port, ok := reservationPort(reservation)
	gatewayUID := types.UID(reservation.Annotations[providerGatewayUIDAnnotation])
	if !ok || gatewayUID == "" || reservation.Labels[providerGatewayUIDLabel] != identityLabelValue(gatewayUID) {
		return ProviderReservationIdentity{}, errors.New("provider port reservation identity is malformed")
	}
	return ProviderReservationIdentity{GatewayUID: gatewayUID, GatewayNamespace: reservation.Namespace, Port: port}, nil
}

func (a ProviderPortAllocator) Reserve(ctx context.Context, lease *wayv1.PortForwardLease, gateway *wayv1.VPNGateway) (uint16, error) {
	if a.Client == nil || lease == nil || gateway == nil || lease.UID == "" || gateway.UID == "" {
		return 0, errors.New("exact lease and gateway identity are required for provider port allocation")
	}
	reservations := &coordinationv1.LeaseList{}
	if err := a.Client.List(ctx, reservations, client.InNamespace(gateway.Namespace), client.MatchingLabels{providerReservationLabel: "true", providerGatewayUIDLabel: identityLabelValue(gateway.UID)}); err != nil {
		return 0, fmt.Errorf("list provider port reservations: %w", err)
	}
	now := a.now()
	occupied := make(map[uint16]struct{}, len(reservations.Items))
	for index := range reservations.Items {
		reservation := &reservations.Items[index]
		if reservation.Annotations[providerGatewayUIDAnnotation] != string(gateway.UID) {
			continue
		}
		port, ok := reservationPort(reservation)
		if !ok {
			return 0, fmt.Errorf("provider port reservation %s is malformed", reservation.Name)
		}
		if reservation.Labels[providerLeaseUIDLabel] == identityLabelValue(lease.UID) && reservation.Annotations[providerLeaseUIDAnnotation] == string(lease.UID) && reservation.Labels[providerStateLabel] == providerStateActive {
			return port, nil
		}
		if reservation.Labels[providerStateLabel] == providerStateQuarantine && quarantineExpired(reservation, now) {
			if err := a.Client.Delete(ctx, reservation); err != nil && !apierrors.IsNotFound(err) {
				return 0, fmt.Errorf("remove expired provider port quarantine: %w", err)
			}
			continue
		}
		occupied[port] = struct{}{}
	}

	count := int(ProviderPortLast-ProviderPortFirst) + 1
	seed := sha256.Sum256([]byte(string(gateway.UID) + "/" + string(lease.UID)))
	start := int(binary.BigEndian.Uint32(seed[:4]) % uint32(count))
	for offset := 0; offset < count; offset++ {
		port := ProviderPortFirst + uint16((start+offset)%count)
		if _, exists := occupied[port]; exists {
			continue
		}
		reservation := desiredProviderReservation(lease, gateway, port, now)
		if err := a.Client.Create(ctx, reservation); err == nil {
			return port, nil
		} else if !apierrors.IsAlreadyExists(err) {
			return 0, fmt.Errorf("create provider port reservation: %w", err)
		}
		existing := &coordinationv1.Lease{}
		if err := a.Client.Get(ctx, client.ObjectKeyFromObject(reservation), existing); err == nil && existing.Labels[providerLeaseUIDLabel] == identityLabelValue(lease.UID) && existing.Annotations[providerLeaseUIDAnnotation] == string(lease.UID) && existing.Labels[providerStateLabel] == providerStateActive {
			return port, nil
		}
	}
	return 0, errors.New("provider internal port range is exhausted")
}

func (a ProviderPortAllocator) Quarantine(ctx context.Context, lease *wayv1.PortForwardLease, gateway *wayv1.VPNGateway, until time.Time) error {
	if a.Client == nil || lease == nil || gateway == nil || lease.UID == "" || gateway.UID == "" || !until.After(a.now()) {
		return errors.New("exact provider reservation and future quarantine deadline are required")
	}
	reservations := &coordinationv1.LeaseList{}
	if err := a.Client.List(ctx, reservations, client.InNamespace(gateway.Namespace), client.MatchingLabels{providerReservationLabel: "true", providerGatewayUIDLabel: identityLabelValue(gateway.UID), providerLeaseUIDLabel: identityLabelValue(lease.UID)}); err != nil {
		return err
	}
	reservations.Items = slices.DeleteFunc(reservations.Items, func(reservation coordinationv1.Lease) bool {
		return reservation.Annotations[providerGatewayUIDAnnotation] != string(gateway.UID) || reservation.Annotations[providerLeaseUIDAnnotation] != string(lease.UID)
	})
	if len(reservations.Items) > 1 {
		return errors.New("multiple provider port reservations exist for one lease UID")
	}
	if len(reservations.Items) == 0 {
		return nil
	}
	reservation := &reservations.Items[0]
	before := reservation.DeepCopy()
	if reservation.Labels == nil {
		reservation.Labels = map[string]string{}
	}
	reservation.Labels[providerStateLabel] = providerStateQuarantine
	nowTime := a.now()
	now := metav1.NewMicroTime(nowTime)
	seconds := int32((until.Sub(nowTime) + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	reservation.Spec.RenewTime = &now
	reservation.Spec.LeaseDurationSeconds = &seconds
	if err := a.Client.Patch(ctx, reservation, client.MergeFrom(before), client.FieldOwner(wayv1.FieldManagerLeaseController)); err != nil {
		return fmt.Errorf("quarantine provider port reservation: %w", err)
	}
	return nil
}

func desiredProviderReservation(lease *wayv1.PortForwardLease, gateway *wayv1.VPNGateway, port uint16, now time.Time) *coordinationv1.Lease {
	holder := string(lease.UID)
	acquired := metav1.NewMicroTime(now)
	return &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name: fmt.Sprintf("waycloak-pf-%s-%d", shortUID(gateway.UID), port), Namespace: gateway.Namespace,
		Labels:      map[string]string{providerReservationLabel: "true", providerGatewayUIDLabel: identityLabelValue(gateway.UID), providerLeaseUIDLabel: identityLabelValue(lease.UID), providerPortLabel: strconv.Itoa(int(port)), providerStateLabel: providerStateActive},
		Annotations: map[string]string{providerGatewayUIDAnnotation: string(gateway.UID), providerLeaseUIDAnnotation: string(lease.UID)},
	}, Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, AcquireTime: &acquired}}
}

func reservationPort(reservation *coordinationv1.Lease) (uint16, bool) {
	value, err := strconv.ParseUint(reservation.Labels[providerPortLabel], 10, 16)
	return uint16(value), err == nil && value >= uint64(ProviderPortFirst) && value <= uint64(ProviderPortLast)
}

func quarantineExpired(reservation *coordinationv1.Lease, now time.Time) bool {
	if reservation.Spec.RenewTime == nil || reservation.Spec.LeaseDurationSeconds == nil {
		return false
	}
	return !reservation.Spec.RenewTime.Add(time.Duration(*reservation.Spec.LeaseDurationSeconds) * time.Second).After(now)
}

func shortUID(uid types.UID) string {
	sum := sha256.Sum256([]byte(uid))
	return fmt.Sprintf("%x", sum[:6])
}

func identityLabelValue(uid types.UID) string {
	sum := sha256.Sum256([]byte(uid))
	return fmt.Sprintf("%x", sum[:16])
}

func (a ProviderPortAllocator) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}
