// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

func TestDeliveryManagerNeutralAndExactAdapterPaths(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	intent := managerIntent("pod-a", 1)
	provider := ProviderObservation{PublicAddress: netip.MustParseAddr("8.8.8.8"), PublicPort: 42000, ExpiresAt: now.Add(time.Minute)}
	neutral := DeliveryManager{Now: func() time.Time { return now }}
	if delivered, acknowledged, err := neutral.Reconcile(context.Background(), intent, provider); err != nil || !delivered || !acknowledged {
		t.Fatalf("neutral delivery = %t/%t, %v", delivered, acknowledged, err)
	}

	intent.AdapterName = "qbittorrent"
	if delivered, acknowledged, err := neutral.Reconcile(context.Background(), intent, provider); err == nil || delivered || acknowledged {
		t.Fatalf("missing adapter delivery = %t/%t, %v", delivered, acknowledged, err)
	}
	adapter := &fakeAdapterProtocol{now: now}
	delivery := DeliveryManager{Adapter: adapter, Now: func() time.Time { return now }}
	if delivered, acknowledged, err := delivery.Reconcile(context.Background(), intent, provider); err != nil || !delivered || !acknowledged {
		t.Fatalf("exact adapter delivery = %t/%t, %v", delivered, acknowledged, err)
	}
	if adapter.record.LeaseUID != intent.LeaseUID || adapter.record.PodUID != intent.PodUID || adapter.record.PublicPort != provider.PublicPort || adapter.record.TargetPort != intent.TargetPort {
		t.Fatalf("adapter record = %#v", adapter.record)
	}

	adapter.stale = true
	if delivered, acknowledged, err := delivery.Reconcile(context.Background(), intent, provider); err != nil || !delivered || acknowledged {
		t.Fatalf("stale adapter acknowledgement = %t/%t, %v", delivered, acknowledged, err)
	}
}

func TestDeliveryManagerWithdrawsOnlyExactReferencedAdapter(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	adapter := &fakeAdapterProtocol{now: now}
	delivery := DeliveryManager{Adapter: adapter, Now: func() time.Time { return now }}
	intent := WithdrawalIntent{APIVersion: RuntimeAPIVersion, LeaseNamespace: "apps", LeaseUID: "lease-uid", GatewayUID: "gateway-uid", HandoffGeneration: 3, PodUID: "pod-uid", AdapterName: "qbittorrent"}
	if withdrawn, err := delivery.Withdraw(context.Background(), intent); err != nil || !withdrawn || !reflect.DeepEqual(adapter.withdrawn, intent) {
		t.Fatalf("adapter withdrawal = %t, %#v, %v", withdrawn, adapter.withdrawn, err)
	}
	intent.AdapterName = ""
	if withdrawn, err := delivery.Withdraw(context.Background(), intent); err != nil || !withdrawn {
		t.Fatalf("neutral withdrawal = %t, %v", withdrawn, err)
	}
}

type fakeAdapterProtocol struct {
	now       time.Time
	record    AdapterLeaseRecord
	withdrawn WithdrawalIntent
	stale     bool
}

func (a *fakeAdapterProtocol) Deliver(_ context.Context, name wayv1.ObjectName, record AdapterLeaseRecord) (AdapterAcknowledgement, error) {
	if name == "" {
		return AdapterAcknowledgement{}, errors.New("adapter name is empty")
	}
	a.record = record
	observedAt := a.now
	if a.stale {
		observedAt = observedAt.Add(-time.Minute)
	}
	return AdapterAcknowledgement{APIVersion: AdapterAPIVersion, LeaseNamespace: record.LeaseNamespace, LeaseUID: record.LeaseUID,
		HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID, ObservedAt: observedAt, ExpiresAt: record.ExpiresAt}, nil
}

func (a *fakeAdapterProtocol) Withdraw(_ context.Context, name wayv1.ObjectName, intent WithdrawalIntent) (bool, error) {
	if name == "" {
		return false, errors.New("adapter name is empty")
	}
	a.withdrawn = intent
	return true, nil
}
