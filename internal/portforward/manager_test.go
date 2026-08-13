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
	"github.com/Amoenus/waycloak/internal/provider"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGatewayRuntimeRequiresDrainBeforeSuccessorAndPreservesProviderIdentity(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	driver := &managerDriver{now: now}
	rules := &managerRules{}
	delivery := &managerDelivery{}
	manager := &GatewayRuntimeManager{PortForward: driver, Rules: rules, Delivery: delivery, Now: func() time.Time { return now }}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: "gateway-uid"}}
	first := managerIntent("pod-a", 1)

	observation, err := manager.Reconcile(context.Background(), gateway, first)
	if err != nil || !observation.GatewayRulesReady || !observation.Delivered || !observation.Acknowledged {
		t.Fatalf("first observation = %#v, %v", observation, err)
	}
	successor := managerIntent("pod-b", 2)
	if _, err := manager.Reconcile(context.Background(), gateway, successor); err == nil {
		t.Fatal("successor was accepted before old rule withdrawal")
	}
	withdrawal := WithdrawalIntent{APIVersion: RuntimeAPIVersion, LeaseNamespace: first.LeaseNamespace, LeaseUID: first.LeaseUID, GatewayUID: first.GatewayUID, HandoffGeneration: 1, ServiceUID: first.ServiceUID, EndpointSliceUID: first.EndpointSliceUID, PodUID: first.PodUID,
		ProviderInternalPort: first.ProviderInternalPort, Protocols: first.Protocols}
	withdrawn, err := manager.Withdraw(context.Background(), gateway, withdrawal)
	if err != nil || !withdrawn.Withdrawn {
		t.Fatalf("drain observation = %#v, %v", withdrawn, err)
	}
	if driver.releaseCalls != 0 {
		t.Fatal("rolling handoff released the stable provider mapping")
	}
	observation, err = manager.Reconcile(context.Background(), gateway, successor)
	if err != nil || !observation.GatewayRulesReady {
		t.Fatalf("successor observation = %#v, %v", observation, err)
	}
	if got, want := rules.replacements, [][]wayv1.ObjectUID{{"lease-uid"}, {}, {"lease-uid"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("atomic rule replacements = %#v, want %#v", got, want)
	}
}

func TestGatewayRuntimeCanReleaseAfterRestartFromDurableWithdrawalIdentity(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	driver := &managerDriver{now: now}
	manager := &GatewayRuntimeManager{PortForward: driver, Rules: &managerRules{}, Delivery: &managerDelivery{}, Now: func() time.Time { return now }}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: "gateway-uid"}}
	intent := managerIntent("pod-a", 7)
	withdrawal := WithdrawalIntent{APIVersion: RuntimeAPIVersion, LeaseNamespace: intent.LeaseNamespace, LeaseUID: intent.LeaseUID, GatewayUID: intent.GatewayUID, HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID,
		ReleaseProvider: true, ProviderInternalPort: intent.ProviderInternalPort, PreviousPublicPort: 42000, Protocols: intent.Protocols}
	observation, err := manager.Withdraw(context.Background(), gateway, withdrawal)
	if err != nil || !observation.Withdrawn || driver.releaseCalls != 1 || driver.lastRequest.InternalPort != intent.ProviderInternalPort {
		t.Fatalf("restart release = %#v, calls=%d request=%#v err=%v", observation, driver.releaseCalls, driver.lastRequest, err)
	}
}

func TestGatewayRuntimeDropsRulesWhenProviderObservationExpires(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	driver := &managerDriver{now: now}
	rules := &managerRules{}
	manager := &GatewayRuntimeManager{PortForward: driver, Rules: rules, Delivery: &managerDelivery{}, Now: func() time.Time { return now }}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: "gateway-uid"}}
	intent := managerIntent("pod-a", 1)
	if _, err := manager.Reconcile(context.Background(), gateway, intent); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	driver.ensureErr = errors.New("provider unavailable")
	if _, err := manager.Reconcile(context.Background(), gateway, intent); err == nil {
		t.Fatal("expired provider mapping remained accepted")
	}
	if got := rules.replacements[len(rules.replacements)-1]; len(got) != 0 {
		t.Fatalf("expired mapping retained rules for %v", got)
	}
}

func TestGatewayRuntimeDoesNotAuthorizeSuccessorUntilRulesAndDeliveryAreWithdrawn(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	rules := &managerRules{}
	delivery := &managerDelivery{}
	manager := &GatewayRuntimeManager{PortForward: &managerDriver{now: now}, Rules: rules, Delivery: delivery, Now: func() time.Time { return now }}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{UID: "gateway-uid"}}
	first := managerIntent("pod-a", 1)
	if _, err := manager.Reconcile(context.Background(), gateway, first); err != nil {
		t.Fatal(err)
	}
	withdrawal := WithdrawalIntent{APIVersion: RuntimeAPIVersion, LeaseNamespace: first.LeaseNamespace, LeaseUID: first.LeaseUID, GatewayUID: first.GatewayUID, HandoffGeneration: 1, PodUID: first.PodUID}

	rules.replaceErr = errors.New("atomic replace failed")
	if _, err := manager.Withdraw(context.Background(), gateway, withdrawal); err == nil {
		t.Fatal("failed rule withdrawal was reported complete")
	}
	if _, err := manager.Reconcile(context.Background(), gateway, managerIntent("pod-b", 2)); err == nil {
		t.Fatal("successor was accepted after failed rule withdrawal")
	}
	rules.replaceErr = nil
	delivery.withdrawPending = true
	if observation, err := manager.Withdraw(context.Background(), gateway, withdrawal); err != nil || observation.Withdrawn {
		t.Fatalf("partial delivery withdrawal = %#v, %v", observation, err)
	}
	if _, err := manager.Reconcile(context.Background(), gateway, managerIntent("pod-b", 2)); err == nil {
		t.Fatal("successor was accepted before delivery withdrawal")
	}
	delivery.withdrawPending = false
	if observation, err := manager.Withdraw(context.Background(), gateway, withdrawal); err != nil || !observation.Withdrawn {
		t.Fatalf("completed withdrawal = %#v, %v", observation, err)
	}
}

func TestGatewayRuntimeTreatsProtocolOrderAsSetAndRejectsDuplicates(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	manager := &GatewayRuntimeManager{PortForward: &managerDriver{now: now}, Rules: &managerRules{}, Delivery: &managerDelivery{}, Now: func() time.Time { return now }}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{UID: "gateway-uid"}}
	intent := managerIntent("pod-a", 1)
	if _, err := manager.Reconcile(context.Background(), gateway, intent); err != nil {
		t.Fatal(err)
	}
	intent.Protocols = []wayv1.TransportProtocol{wayv1.ProtocolUDP, wayv1.ProtocolTCP}
	if _, err := manager.Reconcile(context.Background(), gateway, intent); err != nil {
		t.Fatalf("set-equivalent protocol order changed target identity: %v", err)
	}
	intent.Protocols = []wayv1.TransportProtocol{wayv1.ProtocolTCP, wayv1.ProtocolUDP, wayv1.ProtocolTCP}
	if _, err := manager.Reconcile(context.Background(), gateway, intent); err == nil {
		t.Fatal("oversized duplicate protocol set was accepted")
	}
}

func TestGatewayRuntimeEnforcesObservedProviderCapacityAndWithdrawsOnRegression(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	driver := &managerDriver{now: now}
	rules := &managerRules{}
	manager := &GatewayRuntimeManager{PortForward: driver, Rules: rules, Delivery: &managerDelivery{}, Now: func() time.Time { return now }}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{UID: "gateway-uid"}}
	first := managerIntent("pod-a", 1)
	if _, err := manager.Reconcile(context.Background(), gateway, first); err != nil {
		t.Fatal(err)
	}
	second := managerIntent("pod-b", 1)
	second.LeaseUID = "lease-two"
	if _, err := manager.Reconcile(context.Background(), gateway, second); err == nil {
		t.Fatal("second lease exceeded observed provider capacity")
	}
	if len(rules.current) != 1 || rules.current[0].LeaseUID != first.LeaseUID {
		t.Fatalf("capacity rejection changed active rules: %#v", rules.current)
	}
	driver.capabilities = &provider.PortForwardCapabilities{Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}, MaxLeases: 0, SharedPort: true}
	if _, err := manager.Reconcile(context.Background(), gateway, first); err == nil {
		t.Fatal("provider capability regression remained ready")
	}
	if len(rules.current) != 0 {
		t.Fatalf("capability regression retained inbound rules: %#v", rules.current)
	}
	driver.capabilities = &provider.PortForwardCapabilities{Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}, MaxLeases: 1, SharedPort: true}
	if observation, err := manager.Reconcile(context.Background(), gateway, first); err != nil || !observation.GatewayRulesReady || len(rules.current) != 1 {
		t.Fatalf("capability recovery = %#v, rules=%#v, %v", observation, rules.current, err)
	}
}

func TestGatewayRuntimeUsesProviderPortForExplicitAdapterCapability(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	driver := &managerDriver{now: now}
	rules := &managerRules{}
	delivery := &managerDelivery{}
	manager := &GatewayRuntimeManager{PortForward: driver, Rules: rules, Delivery: delivery, Now: func() time.Time { return now }}
	intent := managerIntent("pod-a", 1)
	intent.AdapterName = "qbittorrent"
	intent.ApplicationPortMode = ApplicationPortProviderAssigned
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{UID: "gateway-uid"}}
	observation, err := manager.Reconcile(context.Background(), gateway, intent)
	if err != nil || !observation.GatewayRulesReady {
		t.Fatalf("provider-assigned application port observation = %#v, %v", observation, err)
	}
	if len(rules.current) != 1 || rules.current[0].TargetPort != 42000 {
		t.Fatalf("provider-assigned gateway rule = %#v", rules.current)
	}
	if delivery.lastIntent.TargetPort != 42000 || delivery.lastIntent.ApplicationPortMode != ApplicationPortProviderAssigned {
		t.Fatalf("provider-assigned adapter intent = %#v", delivery.lastIntent)
	}
}

func managerIntent(podUID wayv1.ObjectUID, generation int64) Intent {
	return Intent{APIVersion: RuntimeAPIVersion, LeaseNamespace: "apps", LeaseUID: "lease-uid", GatewayUID: "gateway-uid", HandoffGeneration: generation,
		ServiceUID: "service-uid", EndpointSliceUID: wayv1.ObjectUID("slice-" + string(podUID)), PodUID: podUID, BindingUID: wayv1.ObjectUID("binding-" + string(podUID)),
		ProviderInternalPort: 50000, OverlayAddress: netip.MustParseAddr("10.42.0.10"), ApplicationAddress: netip.MustParseAddr("192.0.2.10"), BackendPort: 6881, TargetPort: 6881, Protocols: []wayv1.TransportProtocol{wayv1.ProtocolTCP, wayv1.ProtocolUDP},
		ApplicationPortMode: ApplicationPortFixed}
}

type managerDriver struct {
	now          time.Time
	ensureErr    error
	releaseCalls int
	lastRequest  provider.PortForwardLeaseRequest
	capabilities *provider.PortForwardCapabilities
}

func (d *managerDriver) ObserveCapabilities(context.Context) (provider.PortForwardCapabilities, error) {
	if d.capabilities != nil {
		return *d.capabilities, nil
	}
	return provider.PortForwardCapabilities{Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}, MaxLeases: 1, SharedPort: true}, nil
}

func (d *managerDriver) EnsureLease(_ context.Context, request provider.PortForwardLeaseRequest) (provider.PortForwardLeaseObservation, error) {
	d.lastRequest = request
	if d.ensureErr != nil {
		return provider.PortForwardLeaseObservation{}, d.ensureErr
	}
	return provider.PortForwardLeaseObservation{PublicAddress: netip.MustParseAddr("8.8.8.8"), PublicPort: 42000, IssuedAt: d.now, RenewAfter: d.now.Add(30 * time.Second), ExpiresAt: d.now.Add(time.Minute)}, nil
}

func (d *managerDriver) ReleaseLease(_ context.Context, request provider.PortForwardLeaseRequest) error {
	d.releaseCalls++
	d.lastRequest = request
	return nil
}

type managerRules struct {
	current      []GatewayRule
	replacements [][]wayv1.ObjectUID
	replaceErr   error
}

func (r *managerRules) Replace(_ context.Context, rules []GatewayRule) error {
	if r.replaceErr != nil {
		return r.replaceErr
	}
	r.current = append([]GatewayRule(nil), rules...)
	uids := make([]wayv1.ObjectUID, len(rules))
	for index := range rules {
		uids[index] = rules[index].LeaseUID
	}
	r.replacements = append(r.replacements, uids)
	return nil
}

func (r *managerRules) Ready(_ context.Context, rule GatewayRule) (bool, error) {
	for _, current := range r.current {
		if reflect.DeepEqual(current, rule) {
			return true, nil
		}
	}
	return false, nil
}

func (r *managerRules) Withdrawn(_ context.Context, leaseUID wayv1.ObjectUID, _ int64) (bool, error) {
	for _, current := range r.current {
		if current.LeaseUID == leaseUID {
			return false, nil
		}
	}
	return true, nil
}

type managerDelivery struct {
	withdrawPending bool
	lastIntent      Intent
}

func (d *managerDelivery) Reconcile(_ context.Context, intent Intent, _ ProviderObservation) (bool, bool, error) {
	d.lastIntent = intent
	return true, true, nil
}

func (d *managerDelivery) Withdraw(context.Context, WithdrawalIntent) (bool, error) {
	return !d.withdrawPending, nil
}
