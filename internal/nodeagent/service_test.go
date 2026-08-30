// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	waycni "github.com/Amoenus/waycloak/internal/cni"
	"github.com/Amoenus/waycloak/internal/dataplane"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCertificateCapabilityHoldAffectsOnlyPublishedReadiness(t *testing.T) {
	service := &Service{RequireRelay: true, CapabilityHeld: true, NodeName: "node", NodeBootID: "boot", InstanceID: "instance"}
	service.SetBackendHealthy(true)
	service.SetRelayHealthy(true)
	if !service.Ready() || !service.Status().Ready {
		t.Fatal("certificate hold disabled the local CNI readiness boundary")
	}
	if service.Report().Node.Ready {
		t.Fatal("certificate hold published schedulable CNI readiness")
	}
	service.CapabilityHeld = false
	if !service.Report().Node.Ready {
		t.Fatal("released certificate hold did not publish live readiness")
	}
}

func TestTransitionHoldRejectsNewProtectedPathProgramming(t *testing.T) {
	programmer := &fakeProgrammer{}
	service := &Service{Programmer: programmer, CapabilityHeld: true, TransitionHeld: true}
	service.SetBackendHealthy(true)
	service.SetRelayHealthy(true)
	if err := service.Prepare(context.Background(), waycni.PodIdentity{}, waycni.Binding{}); err == nil {
		t.Fatal("transition hold accepted a new protected path")
	}
	if len(programmer.events) != 0 {
		t.Fatalf("transition hold reached packet programming: %v", programmer.events)
	}
}

type fakeProgrammer struct {
	events             []string
	configured         dataplane.Config
	configureErr       error
	verifyErrs         []error
	identity           string
	identityErr        error
	identityErrs       []error
	identityByNetNS    map[string]string
	identityErrByNetNS map[string]error
	lockdownErr        error
}

func (p *fakeProgrammer) Identity(netNS string) (string, error) {
	if err, ok := p.identityErrByNetNS[netNS]; ok {
		return p.identityByNetNS[netNS], err
	}
	if identity, ok := p.identityByNetNS[netNS]; ok {
		return identity, nil
	}
	if p.identity == "" {
		p.identity = "1:2"
	}
	if len(p.identityErrs) > 0 {
		err := p.identityErrs[0]
		p.identityErrs = p.identityErrs[1:]
		return p.identity, err
	}
	return p.identity, p.identityErr
}
func (p *fakeProgrammer) InstallLockdown(context.Context, string, string) error {
	p.events = append(p.events, "lockdown")
	return p.lockdownErr
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
func (s staticAttachments) Delete(waycni.Key) error               { return nil }

type recordingAttachments struct {
	staticAttachments
	deleted   []waycni.Key
	saved     []waycni.Attachment
	deleteErr error
	saveErr   error
}

func (s *recordingAttachments) Delete(key waycni.Key) error {
	s.deleted = append(s.deleted, key)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	retained := s.staticAttachments[:0]
	for _, attachment := range s.staticAttachments {
		if attachment.Key() != key {
			retained = append(retained, attachment)
		}
	}
	s.staticAttachments = retained
	return nil
}

func (s *recordingAttachments) Save(attachment waycni.Attachment) error {
	s.saved = append(s.saved, attachment)
	if s.saveErr != nil {
		return s.saveErr
	}
	for index := range s.staticAttachments {
		if s.staticAttachments[index].Key() == attachment.Key() {
			s.staticAttachments[index] = attachment
			return nil
		}
	}
	s.staticAttachments = append(s.staticAttachments, attachment)
	return nil
}

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

func TestPodLookupFailureMessagesAreSafeAndActionable(t *testing.T) {
	for name, test := range map[string]struct {
		err     error
		message string
		status  int
		code    string
	}{
		"not-found":    {apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "redacted"), "Exact Kubernetes Pod is absent", 404, waycni.AgentErrorPodNotFound},
		"forbidden":    {apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "redacted", errors.New("denied")), "Kubernetes Pod read is unauthorized", 403, waycni.AgentErrorPodIdentityMismatch},
		"unauthorized": {apierrors.NewUnauthorized("redacted"), "Kubernetes API identity is unauthorized", 403, waycni.AgentErrorPodIdentityMismatch},
		"timeout":      {context.DeadlineExceeded, "Kubernetes Pod observation timed out", 403, waycni.AgentErrorPodIdentityMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeServiceError(response, fmt.Errorf("%w: %w", ErrPodLookupFailed, test.err))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.message) || !strings.Contains(response.Body.String(), test.code) || strings.Contains(response.Body.String(), "redacted") {
				t.Fatalf("unsafe lookup response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestWithdrawAbsentPodReportsExactDurableAttachment(t *testing.T) {
	for name, test := range map[string]struct {
		identity    string
		identityErr error
		wantEvents  []string
	}{
		"namespace absent":   {identityErr: fs.ErrNotExist},
		"namespace reused":   {identity: "9:9"},
		"namespace retained": {identity: "1:2", wantEvents: []string{"cleanup"}},
	} {
		t.Run(name, func(t *testing.T) {
			service, identity, reference, programmer := fixture(t)
			published := 0
			service.WithdrawalPublisher = func(_ context.Context, report Report) error {
				published++
				if len(report.Observations) != 1 || report.Observations[0].Ready {
					t.Fatalf("synchronous withdrawal report = %#v", report.Observations)
				}
				return nil
			}
			binding := &wayv1.VPNWorkloadBinding{}
			if err := service.Reader.Get(context.Background(), client.ObjectKey{Namespace: identity.Namespace, Name: waybinding.BindingName(types.UID(identity.UID))}, binding); err != nil {
				t.Fatal(err)
			}
			service.Reader = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(binding).Build()
			service.Store = staticAttachments{{
				Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseLockedDown,
				BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
			}}
			programmer.identity = test.identity
			programmer.identityErr = test.identityErr
			if err := service.Withdraw(context.Background(), identity); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(programmer.events, test.wantEvents) {
				t.Fatalf("withdrawal operations = %v, want %v", programmer.events, test.wantEvents)
			}
			if observations := service.Observations(); len(observations) != 0 {
				t.Fatalf("accepted one-shot withdrawal remained queued: %#v", observations)
			}
			if published != 1 {
				t.Fatalf("synchronous withdrawal publications = %d", published)
			}
		})
	}
}

func TestWithdrawDoesNotAcknowledgeRejectedObservation(t *testing.T) {
	service, identity, reference, _ := fixture(t)
	service.Store = staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}}
	service.WithdrawalPublisher = func(context.Context, Report) error { return errors.New("relay unavailable") }
	if err := service.Withdraw(context.Background(), identity); err == nil || !strings.Contains(err.Error(), "publish exact attachment withdrawal") {
		t.Fatalf("rejected withdrawal publication = %v", err)
	}
	if observations := service.Observations(); len(observations) != 1 || observations[0].Ready {
		t.Fatalf("rejected withdrawal lost fail-closed observation: %#v", observations)
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

func TestTransitionHoldReconciliationNeverReopensDurableAttachment(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	service.CapabilityHeld = true
	service.TransitionHeld = true
	service.Store = staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}}
	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("transition-hold operations = %v, want %v", programmer.events, want)
	}
	if observations := service.Observations(); len(observations) != 1 || observations[0].Ready {
		t.Fatalf("transition hold did not publish exact withdrawal: %#v", observations)
	}
}

func TestTransitionHoldWithdrawsNodeBindingWithoutDurableAttachment(t *testing.T) {
	service, _, _, programmer := fixture(t)
	service.CapabilityHeld = true
	service.TransitionHeld = true
	service.Store = staticAttachments{}
	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(programmer.events) != 0 {
		t.Fatalf("binding without attachment mutated packet state: %v", programmer.events)
	}
	observations := service.Observations()
	if len(observations) != 1 || observations[0].Ready || observations[0].BindingUID != "binding-uid" ||
		observations[0].InstanceID != "agent-a" {
		t.Fatalf("transition hold did not cover binding without attachment: %#v", observations)
	}
}

func TestAuthenticatedRestartRecoveryBypassesOnlyPublicReadinessGate(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	service.RequireRelay = true
	service.SetRelayHealthy(false)
	service.SetBackendHealthy(false)
	service.Store = staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}}
	if err := service.Check(context.Background(), identity, reference); err == nil {
		t.Fatal("public CNI check succeeded before restart recovery")
	}
	if want := []string{"lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("public readiness gate operations = %v, want %v", programmer.events, want)
	}
	programmer.events = nil
	service.SetRelayHealthy(true)
	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"verify"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("authenticated restart recovery operations = %v, want %v", programmer.events, want)
	}
	if service.Ready() {
		t.Fatal("internal reconciliation published readiness before its caller committed backend health")
	}
	if err := service.Check(context.Background(), identity, reference); err == nil {
		t.Fatal("public CNI check bypassed the still-false backend state")
	}
	if want := []string{"verify", "lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("post-recovery public gate operations = %v, want %v", programmer.events, want)
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

func TestDriftReconciliationDoesNotRaceFreshLockedDownADD(t *testing.T) {
	for _, offset := range []time.Duration{-lockedDownReconcileDelay + time.Second, time.Second} {
		service, identity, _, programmer := fixture(t)
		service.Store = staticAttachments{{
			Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseLockedDown,
			UpdatedAt: service.now().Add(offset),
		}}

		if err := service.ReconcileAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(programmer.events) != 0 {
			t.Fatalf("fresh or future-dated CNI ADD was raced by drift reconciliation: %v", programmer.events)
		}
	}
}

func TestDriftReconciliationReassertsAbandonedLockedDownState(t *testing.T) {
	service, identity, _, programmer := fixture(t)
	service.Store = staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseLockedDown,
		UpdatedAt: service.now().Add(-lockedDownReconcileDelay),
	}}

	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("abandoned ADD operations = %v, want %v", programmer.events, want)
	}
}

func TestRestartRecoveryRetainsLivePodEnrollmentWhenNamespaceIsMissing(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	store := &recordingAttachments{staticAttachments: staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}}}
	service.Store = store
	programmer.identityErr = fs.ErrNotExist

	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(programmer.events) != 0 {
		t.Fatalf("missing namespace was programmed: %v", programmer.events)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("live Pod enrollment was discarded: %#v", store.deleted)
	}
	if observations := service.Observations(); len(observations) != 1 || observations[0].Ready {
		t.Fatalf("missing namespace observation = %#v", observations)
	}
}

func TestRestartRecoveryRetainsLivePodEnrollmentWithoutTouchingReusedNamespace(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	store := &recordingAttachments{staticAttachments: staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}}}
	service.Store = store
	programmer.identity = "9:9"

	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(programmer.events) != 0 {
		t.Fatalf("replacement namespace was touched: %v", programmer.events)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("live Pod enrollment was discarded: %#v", store.deleted)
	}
	if observations := service.Observations(); len(observations) != 1 || observations[0].Ready {
		t.Fatalf("reused namespace observation = %#v", observations)
	}
}

func TestRestartRecoveryWithdrawsFailedADDAfterExactPodDisappears(t *testing.T) {
	for name, replacementPod := range map[string]bool{"not found": false, "name reused by new UID": true} {
		t.Run(name, func(t *testing.T) {
			service, identity, reference, programmer := fixture(t)
			binding := &wayv1.VPNWorkloadBinding{}
			if err := service.Reader.Get(context.Background(), client.ObjectKey{Namespace: identity.Namespace, Name: waybinding.BindingName(types.UID(identity.UID))}, binding); err != nil {
				t.Fatal(err)
			}
			objects := []client.Object{binding}
			if replacementPod {
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: identity.Name, Namespace: identity.Namespace, UID: "replacement-uid"}, Spec: corev1.PodSpec{NodeName: "node-a"}})
			}
			service.Reader = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objects...).Build()
			store := &recordingAttachments{staticAttachments: staticAttachments{{
				Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseLockedDown,
			}}}
			service.Store = store
			programmer.identity = "9:9"
			published := 0
			service.WithdrawalPublisher = func(_ context.Context, report Report) error {
				published++
				if len(report.Observations) != 1 || report.Observations[0].Ready || report.Observations[0].BindingUID != reference.UID {
					t.Fatalf("absent Pod withdrawal report = %#v", report.Observations)
				}
				return nil
			}

			if err := service.ReconcileAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(programmer.events) != 0 {
				t.Fatalf("missing or reused namespace was touched: %v", programmer.events)
			}
			if len(store.deleted) != 1 || store.deleted[0] != identityKey(identity) || published != 1 {
				t.Fatalf("withdrawal state: deleted=%#v published=%d", store.deleted, published)
			}
			if observations := service.Observations(); len(observations) != 0 {
				t.Fatalf("accepted withdrawal was replayable: %#v", observations)
			}
		})
	}
}

func TestRestartRecoveryRetainsStateWhenWithdrawnAttachmentCannotBeDeleted(t *testing.T) {
	service, identity, reference, _ := fixture(t)
	binding := &wayv1.VPNWorkloadBinding{}
	if err := service.Reader.Get(context.Background(), client.ObjectKey{Namespace: identity.Namespace, Name: waybinding.BindingName(types.UID(identity.UID))}, binding); err != nil {
		t.Fatal(err)
	}
	service.Reader = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(binding).Build()
	store := &recordingAttachments{staticAttachments: staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}}, deleteErr: errors.New("read-only state")}
	service.Store = store
	service.WithdrawalPublisher = func(context.Context, Report) error { return nil }

	if err := service.ReconcileAll(context.Background()); err == nil || !strings.Contains(err.Error(), "delete withdrawn Pod attachment") {
		t.Fatalf("delete failure = %v", err)
	}
}

func TestRestartRecoveryAbsorbsNamespaceDisappearanceDuringRepair(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	store := &recordingAttachments{staticAttachments: staticAttachments{{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}}}
	service.Store = store
	programmer.verifyErrs = []error{errors.New("drift")}
	programmer.lockdownErr = fs.ErrNotExist
	programmer.identityErrs = []error{nil, fs.ErrNotExist}

	if err := service.ReconcileAll(context.Background()); err == nil || !strings.Contains(err.Error(), "restore deny-first state after drift") {
		t.Fatalf("namespace disappearance = %v", err)
	}
	if want := []string{"verify", "lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("repair race operations = %v, want %v", programmer.events, want)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("live Pod enrollment was discarded: %#v", store.deleted)
	}
}

func TestRestartRecoveryAggregatesFailedADDRetriesByExactPodUID(t *testing.T) {
	for _, readyFirst := range []bool{true, false} {
		name := "ready-last"
		if readyFirst {
			name = "ready-first"
		}
		t.Run(name, func(t *testing.T) {
			service, identity, reference, programmer := fixture(t)
			identity.ContainerID = "sandbox-current"
			identity.NetNS = "/proc/current/ns/net"
			current := waycni.Attachment{
				Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
				BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
				UpdatedAt: service.now(),
			}
			stale := make([]waycni.Attachment, 0, 20)
			programmer.identityByNetNS = map[string]string{identity.NetNS: current.NamespaceIdentity}
			programmer.identityErrByNetNS = make(map[string]error, 20)
			for attempt := 0; attempt < 20; attempt++ {
				failedIdentity := identity
				failedIdentity.ContainerID = fmt.Sprintf("failed-%02d", attempt)
				failedIdentity.NetNS = fmt.Sprintf("/proc/failed-%02d/ns/net", attempt)
				stale = append(stale, waycni.Attachment{
					Network: "kindnet", Pod: failedIdentity, NamespaceIdentity: fmt.Sprintf("old:%d", attempt),
					Phase: waycni.PhaseLockedDown, UpdatedAt: service.now().Add(-lockedDownReconcileDelay),
				})
				programmer.identityErrByNetNS[failedIdentity.NetNS] = fs.ErrNotExist
			}
			attachments := append([]waycni.Attachment(nil), stale...)
			if readyFirst {
				attachments = append([]waycni.Attachment{current}, attachments...)
			} else {
				attachments = append(attachments, current)
			}
			store := &recordingAttachments{staticAttachments: attachments}
			service.Store = store

			if err := service.ReconcileAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if want := []string{"verify"}; !reflect.DeepEqual(programmer.events, want) {
				t.Fatalf("group recovery operations = %v, want %v", programmer.events, want)
			}
			if observations := service.Observations(); len(observations) != 1 || !observations[0].Ready {
				t.Fatalf("verified Ready sandbox was overridden: %#v", observations)
			}
			if len(store.deleted) != len(stale) || len(store.staticAttachments) != 1 || store.staticAttachments[0].Key() != current.Key() {
				t.Fatalf("quarantine cleanup: deleted=%d retained=%#v", len(store.deleted), store.staticAttachments)
			}

			if err := service.ReconcileAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if observations := service.Observations(); len(observations) != 1 || !observations[0].Ready {
				t.Fatalf("idempotent restart observation = %#v", observations)
			}
			if len(store.deleted) != len(stale) || len(store.staticAttachments) != 1 {
				t.Fatalf("idempotent cleanup repeated: deleted=%d retained=%d", len(store.deleted), len(store.staticAttachments))
			}
		})
	}
}

func TestFileStoreRestartRecoveryConvergesToOneDurableLiveAttachment(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	identity.ContainerID = "sandbox-current"
	identity.NetNS = "/proc/current/ns/net"
	current := waycni.Attachment{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
		UpdatedAt: service.now(),
	}
	store := waycni.FileStore{Directory: t.TempDir()}
	mustSave := func(attachment waycni.Attachment) {
		t.Helper()
		if err := store.Save(attachment); err != nil {
			t.Fatal(err)
		}
	}
	mustSave(current)
	programmer.identityByNetNS = map[string]string{identity.NetNS: current.NamespaceIdentity}
	programmer.identityErrByNetNS = make(map[string]error, 20)
	for attempt := 0; attempt < 20; attempt++ {
		failedIdentity := identity
		failedIdentity.ContainerID = fmt.Sprintf("failed-%02d", attempt)
		failedIdentity.NetNS = fmt.Sprintf("/proc/failed-%02d/ns/net", attempt)
		mustSave(waycni.Attachment{
			Network: "kindnet", Pod: failedIdentity, NamespaceIdentity: fmt.Sprintf("old:%d", attempt),
			Phase: waycni.PhaseLockedDown, UpdatedAt: service.now().Add(-lockedDownReconcileDelay),
		})
		programmer.identityErrByNetNS[failedIdentity.NetNS] = fs.ErrNotExist
	}
	service.Store = store

	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	retained, err := store.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 1 || retained[0].Key() != current.Key() || retained[0].Phase != waycni.PhaseReady {
		t.Fatalf("durable state after restart = %#v", retained)
	}
	if observations := service.Observations(); len(observations) != 1 || !observations[0].Ready {
		t.Fatalf("durable restart observation = %#v", observations)
	}
}

func TestRestartRecoveryPreservesYoungFailedADDQuarantineAfterReadyProof(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	identity.ContainerID = "sandbox-current"
	identity.NetNS = "/proc/current/ns/net"
	current := waycni.Attachment{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}
	failedIdentity := identity
	failedIdentity.ContainerID = "failed-young"
	failedIdentity.NetNS = "/proc/failed-young/ns/net"
	young := waycni.Attachment{
		Network: "kindnet", Pod: failedIdentity, NamespaceIdentity: "old:1", Phase: waycni.PhaseLockedDown,
		UpdatedAt: service.now().Add(-lockedDownReconcileDelay + time.Second),
	}
	programmer.identityByNetNS = map[string]string{identity.NetNS: current.NamespaceIdentity}
	programmer.identityErrByNetNS = map[string]error{failedIdentity.NetNS: fs.ErrNotExist}
	store := &recordingAttachments{staticAttachments: staticAttachments{young, current}}
	service.Store = store

	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.deleted) != 0 || len(store.staticAttachments) != 2 {
		t.Fatalf("young failed ADD was removed during its grace period: deleted=%#v", store.deleted)
	}
	if observations := service.Observations(); len(observations) != 1 || !observations[0].Ready {
		t.Fatalf("verified Ready observation = %#v", observations)
	}
}

func TestRestartRecoveryRetainsQuarantineUntilReadySandboxIsVerified(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	identity.ContainerID = "sandbox-current"
	identity.NetNS = "/proc/current/ns/net"
	current := waycni.Attachment{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}
	failedIdentity := identity
	failedIdentity.ContainerID = "failed-old"
	failedIdentity.NetNS = "/proc/failed-old/ns/net"
	old := waycni.Attachment{
		Network: "kindnet", Pod: failedIdentity, NamespaceIdentity: "old:1", Phase: waycni.PhaseLockedDown,
		UpdatedAt: service.now().Add(-lockedDownReconcileDelay),
	}
	programmer.identityByNetNS = map[string]string{identity.NetNS: current.NamespaceIdentity}
	programmer.identityErrByNetNS = map[string]error{failedIdentity.NetNS: fs.ErrNotExist}
	programmer.verifyErrs = []error{errors.New("drift"), errors.New("repair did not converge")}
	store := &recordingAttachments{staticAttachments: staticAttachments{old, current}}
	service.Store = store

	if err := service.ReconcileAll(context.Background()); err == nil {
		t.Fatal("unverified Ready sandbox released its failed-ADD quarantine")
	}
	if len(store.deleted) != 0 || len(store.staticAttachments) != 2 {
		t.Fatalf("state was cleaned before current sandbox proof: deleted=%#v retained=%d", store.deleted, len(store.staticAttachments))
	}
	if observations := service.Observations(); len(observations) != 1 || observations[0].Ready {
		t.Fatalf("unverified sandbox observation = %#v", observations)
	}
}

func TestRestartRecoveryFailsClosedForMultipleLiveSandboxes(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	first := waycni.Attachment{
		Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady,
		BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID,
	}
	secondIdentity := identity
	secondIdentity.ContainerID = "replacement-sandbox"
	secondIdentity.NetNS = "/proc/2/ns/net"
	second := first
	second.Pod = secondIdentity
	second.NamespaceIdentity = "3:4"
	programmer.identityByNetNS = map[string]string{identity.NetNS: first.NamespaceIdentity, secondIdentity.NetNS: second.NamespaceIdentity}
	store := &recordingAttachments{staticAttachments: staticAttachments{first, second}}
	service.Store = store

	err := service.ReconcileAll(context.Background())
	if !errors.Is(err, ErrSandboxAmbiguous) {
		t.Fatalf("multiple live sandbox result = %v", err)
	}
	if want := []string{"lockdown", "lockdown"}; !reflect.DeepEqual(programmer.events, want) {
		t.Fatalf("ambiguous sandbox operations = %v, want %v", programmer.events, want)
	}
	if observations := service.Observations(); len(observations) != 1 || observations[0].Ready {
		t.Fatalf("ambiguous sandbox observation = %#v", observations)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("ambiguous sandbox state was discarded: %#v", store.deleted)
	}
}

func TestRestartRecoveryWithdrawsPodUIDGroupOnceAfterExactPodAbsence(t *testing.T) {
	service, identity, reference, programmer := fixture(t)
	binding := &wayv1.VPNWorkloadBinding{}
	if err := service.Reader.Get(context.Background(), client.ObjectKey{Namespace: identity.Namespace, Name: waybinding.BindingName(types.UID(identity.UID))}, binding); err != nil {
		t.Fatal(err)
	}
	service.Reader = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(binding).Build()
	secondIdentity := identity
	secondIdentity.ContainerID = "failed-retry"
	secondIdentity.NetNS = "/proc/missing/ns/net"
	attachments := staticAttachments{
		{Network: "kindnet", Pod: identity, NamespaceIdentity: "1:2", Phase: waycni.PhaseReady, BindingUID: reference.UID, BindingGeneration: reference.Generation, GatewayUID: reference.GatewayUID},
		{Network: "kindnet", Pod: secondIdentity, NamespaceIdentity: "3:4", Phase: waycni.PhaseLockedDown},
	}
	programmer.identityErrByNetNS = map[string]error{identity.NetNS: fs.ErrNotExist, secondIdentity.NetNS: fs.ErrNotExist}
	store := &recordingAttachments{staticAttachments: attachments}
	service.Store = store
	published := 0
	service.WithdrawalPublisher = func(_ context.Context, report Report) error {
		published++
		if len(report.Observations) != 1 || report.Observations[0].Ready {
			t.Fatalf("group withdrawal report = %#v", report.Observations)
		}
		return nil
	}

	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if published != 1 || len(store.deleted) != 2 || len(store.staticAttachments) != 0 {
		t.Fatalf("group withdrawal: published=%d deleted=%d retained=%d", published, len(store.deleted), len(store.staticAttachments))
	}
	if observations := service.Observations(); len(observations) != 0 {
		t.Fatalf("withdrawn group remained observable: %#v", observations)
	}
	if err := service.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if published != 1 || len(store.deleted) != 2 {
		t.Fatalf("idempotent group withdrawal repeated: published=%d deleted=%d", published, len(store.deleted))
	}
}

func identityKey(identity waycni.PodIdentity) waycni.Key {
	return waycni.Key{Network: "kindnet", ContainerID: identity.ContainerID, IfName: identity.IfName}
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
