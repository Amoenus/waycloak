// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	waycni "github.com/Amoenus/waycloak/internal/cni"
	"github.com/Amoenus/waycloak/internal/dataplane"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeProgrammer struct {
	events       []string
	configured   dataplane.Config
	configureErr error
	verifyErrs   []error
}

func (p *fakeProgrammer) Identity(string) (string, error) { return "1:2", nil }
func (p *fakeProgrammer) InstallLockdown(context.Context, string, string) error {
	p.events = append(p.events, "lockdown")
	return nil
}
func (p *fakeProgrammer) Configure(_ context.Context, _ string, cfg dataplane.Config) error {
	p.events = append(p.events, "configure")
	p.configured = cfg
	return p.configureErr
}
func (p *fakeProgrammer) Verify(context.Context, string, dataplane.Config) error {
	p.events = append(p.events, "verify")
	if len(p.verifyErrs) == 0 {
		return nil
	}
	err := p.verifyErrs[0]
	p.verifyErrs = p.verifyErrs[1:]
	return err
}
func (p *fakeProgrammer) Cleanup(context.Context, string, string) error {
	p.events = append(p.events, "cleanup")
	return nil
}

type staticAttachments []waycni.Attachment

func (s staticAttachments) ListAll() ([]waycni.Attachment, error) { return s, nil }
func (s staticAttachments) Save(waycni.Attachment) error          { return nil }

func TestPrepareUsesControllerBindingAndVerifiesBeforeReady(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	if err := service.Prepare(context.Background(), identity, reference); err != nil {
		t.Fatal(err)
	}
	if want := []string{"lockdown", "configure", "verify"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("programming order = %v, want %v", programmer.events, want)
	}
	if programmer.configured.PodUID != identity.UID || programmer.configured.GatewayGeneration != 4 || programmer.configured.GatewayEndpoint.String() != "198.51.100.2:4789" {
		t.Fatalf("derived config = %#v", programmer.configured)
	}
	observations := service.Observations()
	if len(observations) != 1 || !observations[0].Ready || observations[0].BindingUID != reference.UID {
		t.Fatalf("observations = %#v", observations)
	}
}

func TestPrepareRejectsCallerBindingSubstitutionBeforeProgramming(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	reference.Generation++
	if err := service.Prepare(context.Background(), identity, reference); err == nil {
		t.Fatal("stale caller generation was accepted")
	}
	if len(programmer.events) != 0 {
		t.Fatalf("untrusted request reached programming: %v", programmer.events)
	}
}

func TestPodAuthorityFailuresRemainDistinctAndFailClosed(t *testing.T) {
	service, identity, _, _ := fixture(t)

	wrongUID := identity
	wrongUID.UID = "other-uid"
	if _, err := service.Resolve(context.Background(), wrongUID); !errors.Is(err, ErrPodUIDMismatch) {
		t.Fatalf("UID mismatch = %v", err)
	}

	service.NodeName = "other-node"
	if _, err := service.Resolve(context.Background(), identity); !errors.Is(err, ErrPodNodeMismatch) {
		t.Fatalf("node mismatch = %v", err)
	}

	service.NodeName = "node-a"
	missing := identity
	missing.Name = "missing"
	if _, err := service.Resolve(context.Background(), missing); !errors.Is(err, ErrPodLookupFailed) {
		t.Fatalf("Pod lookup failure = %v", err)
	}
}

func TestPrepareFailureRestoresLockdownAndNeverReportsReady(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	programmer.verifyErrs = []error{errors.New("gateway unhealthy")}
	if err := service.Prepare(context.Background(), identity, reference); err == nil {
		t.Fatal("unhealthy gateway passed prepare")
	}
	if want := []string{"lockdown", "configure", "verify", "lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("failure order = %v, want %v", programmer.events, want)
	}
	if observations := service.Observations(); len(observations) != 1 || observations[0].Ready {
		t.Fatalf("failure observations = %#v", observations)
	}
}

func TestCheckRepairsDriftUnderLockdown(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	programmer.verifyErrs = []error{errors.New("drift"), nil}
	if err := service.Check(context.Background(), identity, reference); err != nil {
		t.Fatal(err)
	}
	if want := []string{"verify", "lockdown", "configure", "verify"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("repair order = %v, want %v", programmer.events, want)
	}
}

func TestControllerRelayLossRejectsPrepareAndWithdrawsEveryAttachment(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	service.RequireRelay = true
	service.Store = staticAttachments{{Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady, BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID}}
	if err := service.Prepare(context.Background(), identity, reference); err == nil {
		t.Fatal("prepare succeeded before authenticated controller contact")
	}
	if err := service.LockdownAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("controller-loss operations = %v, want %v", programmer.events, want)
	}
	service.SetRelayHealthy(true)
	if err := service.Prepare(context.Background(), identity, reference); err != nil {
		t.Fatal(err)
	}
}

func TestRestartRecoveryLocksRevokedBindingAndCleansOnlyAbsentPod(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	service.Store = staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: "revoked-binding", BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}}
	if err := service.ReconcileAll(context.Background()); err == nil {
		t.Fatal("revoked generation did not fail restart recovery")
	}
	if want := []string{"lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("revoked recovery operations = %v", programmer.events)
	}

	service.Reader = fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	programmer.events = nil
	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"cleanup"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("stale namespace cleanup = %v", programmer.events)
	}
}

func TestRestartRecoveryAdoptsNewGenerationOnlyAfterVerification(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	service.Store = staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation - 1, GatewayUID: reference.GatewayUID,
	}}
	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"lockdown", "configure", "verify"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("generation adoption operations = %v, want %v", programmer.events, want)
	}
}

func fixture(t *testing.T) (*Service, waycni.PodIdentity, waycni.Binding, *fakeProgrammer) {
	t.Helper()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: "apps", UID: "pod-uid", Labels: map[string]string{routeLabel: "private"}}, Spec: corev1.PodSpec{NodeName: "node-a"}}
	binding := &wayv1.VPNWorkloadBinding{ObjectMeta: metav1.ObjectMeta{Name: waybinding.BindingName(pod.UID), Namespace: pod.Namespace, UID: "binding-uid", Generation: 3}, Spec: wayv1.VPNWorkloadBindingSpec{
		PodRef: wayv1.LocalUIDReference{Name: "protected", UID: "pod-uid"}, RouteRef: wayv1.LocalUIDReference{Name: "private", UID: "route-uid"},
		GatewayRef: wayv1.NamespacedUIDReference{Namespace: "network", Name: "gateway", UID: "gateway-uid"}, NodeName: "node-a",
		Allocation: wayv1.WorkloadAllocation{Identity: "allocation", Address: "192.0.2.2/32"},
		Network:    wayv1.WorkloadNetworkIntent{GatewayGeneration: 4, OverlayCIDR: "192.0.2.0/29", GatewayAddress: "192.0.2.1", GatewayEndpoint: "198.51.100.2:4789", GatewayHealthPort: 18080, VNI: 7999, MTU: 1320, ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll}},
	}}
	reader := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod, binding).Build()
	programmer := &fakeProgrammer{}
	service := &Service{Reader: reader, Programmer: programmer, Store: staticAttachments{}, NodeName: "node-a", NodeBootID: "boot-a", InstanceID: "agent-a", Now: func() time.Time { return time.Unix(1000, 0).UTC() }}
	service.SetBackendHealthy(true)
	identity := waycni.PodIdentity{Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID), ContainerID: "sandbox", IfName: "eth0", NetNS: "/proc/1/ns/net"}
	reference := bindingReference(binding)
	return service, identity, reference, programmer
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
