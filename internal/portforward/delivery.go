// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"errors"
	"net/netip"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

const AdapterAPIVersion = "networking.waycloak.io/adapter/v1"

// AdapterLeaseRecord is the complete renewable input to an out-of-process,
// unprivileged adapter. It intentionally contains neither Kubernetes nor VPN
// credentials and grants no packet-programming authority.
type AdapterLeaseRecord struct {
	APIVersion         string                    `json:"apiVersion"`
	LeaseNamespace     wayv1.NamespaceName       `json:"leaseNamespace"`
	LeaseUID           wayv1.ObjectUID           `json:"leaseUID"`
	HandoffGeneration  int64                     `json:"handoffGeneration"`
	PodUID             wayv1.ObjectUID           `json:"podUID"`
	PublicAddress      netip.Addr                `json:"publicAddress"`
	PublicPort         uint16                    `json:"publicPort"`
	ExpiresAt          time.Time                 `json:"expiresAt"`
	TargetPort         uint16                    `json:"targetPort"`
	ApplicationAddress netip.Addr                `json:"applicationAddress"`
	BackendPort        uint16                    `json:"backendPort"`
	Protocols          []wayv1.TransportProtocol `json:"protocols"`
}

type AdapterAcknowledgement struct {
	APIVersion        string              `json:"apiVersion"`
	LeaseNamespace    wayv1.NamespaceName `json:"leaseNamespace"`
	LeaseUID          wayv1.ObjectUID     `json:"leaseUID"`
	HandoffGeneration int64               `json:"handoffGeneration"`
	PodUID            wayv1.ObjectUID     `json:"podUID"`
	ObservedAt        time.Time           `json:"observedAt"`
	ExpiresAt         time.Time           `json:"expiresAt"`
}

type AdapterProtocol interface {
	Deliver(context.Context, wayv1.ObjectName, AdapterLeaseRecord) (AdapterAcknowledgement, error)
	Withdraw(context.Context, wayv1.ObjectName, WithdrawalIntent) (bool, error)
}

// DeliveryManager makes the neutral no-adapter path explicit. Adapter-backed
// intent is never treated as delivered unless the exact generation and Pod UID
// are acknowledged by the configured out-of-process protocol peer.
type DeliveryManager struct {
	Adapter AdapterProtocol
	Now     func() time.Time
}

func (d DeliveryManager) Reconcile(ctx context.Context, intent Intent, provider ProviderObservation) (bool, bool, error) {
	if err := validDeliveryInput(intent, provider, d.now()); err != nil {
		return false, false, err
	}
	if intent.AdapterName == "" {
		return true, true, nil
	}
	if d.Adapter == nil {
		return false, false, errors.New("application adapter protocol is unavailable")
	}
	record := AdapterLeaseRecord{APIVersion: AdapterAPIVersion, LeaseNamespace: intent.LeaseNamespace, LeaseUID: intent.LeaseUID,
		HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID, PublicAddress: provider.PublicAddress, PublicPort: provider.PublicPort,
		ExpiresAt: provider.ExpiresAt.UTC(), TargetPort: intent.TargetPort, ApplicationAddress: intent.ApplicationAddress, BackendPort: intent.BackendPort,
		Protocols: append([]wayv1.TransportProtocol(nil), intent.Protocols...)}
	acknowledgement, err := d.Adapter.Deliver(ctx, intent.AdapterName, record)
	if err != nil {
		return false, false, err
	}
	acknowledged := acknowledgement.APIVersion == AdapterAPIVersion && acknowledgement.LeaseNamespace == record.LeaseNamespace && acknowledgement.LeaseUID == record.LeaseUID &&
		acknowledgement.HandoffGeneration == record.HandoffGeneration && acknowledgement.PodUID == record.PodUID && !acknowledgement.ObservedAt.IsZero() &&
		!acknowledgement.ObservedAt.Before(d.now().Add(-DefaultObservationFreshness)) && !acknowledgement.ObservedAt.After(d.now().Add(time.Minute)) && acknowledgement.ExpiresAt.Equal(record.ExpiresAt)
	return true, acknowledged, nil
}

func (d DeliveryManager) Withdraw(ctx context.Context, intent WithdrawalIntent) (bool, error) {
	if intent.AdapterName == "" {
		return true, nil
	}
	if d.Adapter == nil {
		return false, errors.New("application adapter protocol is unavailable")
	}
	return d.Adapter.Withdraw(ctx, intent.AdapterName, intent)
}

func validDeliveryInput(intent Intent, provider ProviderObservation, now time.Time) error {
	if intent.LeaseNamespace == "" || intent.LeaseUID == "" || intent.HandoffGeneration < 1 || intent.PodUID == "" || intent.BackendPort == 0 || intent.TargetPort == 0 ||
		!intent.ApplicationAddress.Is4() || !provider.PublicAddress.IsValid() || !provider.PublicAddress.IsGlobalUnicast() || provider.PublicPort == 0 || !provider.ExpiresAt.After(now) {
		return errors.New("lease delivery input is invalid")
	}
	return nil
}

func (d DeliveryManager) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

var _ DeliveryBackend = DeliveryManager{}
