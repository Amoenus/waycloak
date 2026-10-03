// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"context"
	"errors"
	"strings"
	"testing"

	installv1 "github.com/Amoenus/waycloak/api/installation/v1alpha1"
	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var testPhases = []string{waycloakctl.NativePhaseHold, waycloakctl.NativePhaseClass, waycloakctl.NativePhaseStage, waycloakctl.NativePhaseGateways, waycloakctl.NativePhaseActivate, waycloakctl.NativePhaseVerify, waycloakctl.NativePhaseComplete}

type recordingBackend struct {
	prepared    []installv1.WaycloakInstallationSpec
	advanced    []string
	fail        string
	verifyError error
}

func (b *recordingBackend) Prepare(_ context.Context, spec installv1.WaycloakInstallationSpec) (waycloakctl.NativeInstallPlan, error) {
	b.prepared = append(b.prepared, spec)
	return waycloakctl.NativeInstallPlan{Plan: waycloakctl.InstallPlan{Namespace: spec.Namespace, Release: spec.Release, PlanID: "sha256:" + strings.Repeat("a", 64)}}, nil
}
func (b *recordingBackend) Advance(_ context.Context, _ types.UID, _ waycloakctl.NativeInstallPlan, phase string) (string, error) {
	b.advanced = append(b.advanced, phase)
	if b.fail == phase {
		return "", errors.New("simulated interruption")
	}
	for i, p := range testPhases[:len(testPhases)-1] {
		if phase == p {
			return testPhases[i+1], nil
		}
	}
	return "", errors.New("unknown phase")
}
func (b *recordingBackend) Verify(context.Context, waycloakctl.NativeInstallPlan) error {
	return b.verifyError
}

func reconciliationFixture(t *testing.T) (*Reconciler, *recordingBackend, *installv1.WaycloakInstallation) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := installv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	installation := &installv1.WaycloakInstallation{ObjectMeta: metav1.ObjectMeta{Name: "waycloak", UID: "original-installation", Generation: 1}, Spec: installv1.WaycloakInstallationSpec{Version: "v1.0.2-rc.4", Namespace: "waycloak-system", Release: "waycloak"}}
	backend := new(recordingBackend)
	return &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(installation).WithObjects(installation).Build(), Clients: &waycloakctl.Clients{Kubernetes: kubefake.NewClientset()}, Namespace: "waycloak-system", Backend: backend}, backend, installation
}

func reconcileOnce(t *testing.T, r *Reconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waycloak"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRestartAtEveryPhaseRetainsImmutableIntent(t *testing.T) {
	for _, stopped := range testPhases[:len(testPhases)-1] {
		t.Run(stopped, func(t *testing.T) {
			r, b, installation := reconciliationFixture(t)
			reconcileOnce(t, r)
			for _, phase := range testPhases[:len(testPhases)-1] {
				if phase == stopped {
					break
				}
				reconcileOnce(t, r)
			}
			before, record, err := r.loadJournal(context.Background(), installation)
			if err != nil {
				t.Fatal(err)
			}
			b.fail = stopped
			reconcileOnce(t, r)
			after, unchanged, err := r.loadJournal(context.Background(), installation)
			if err != nil || unchanged.Annotations[phaseKey] != stopped || after.IntentDigest != before.IntentDigest || unchanged.Data["journal.json"] != record.Data["journal.json"] {
				t.Fatal("interruption advanced or replaced journal", err)
			}
			// A new process has no in-memory plan or phase; it resumes from API state.
			restartedBackend := new(recordingBackend)
			restarted := &Reconciler{Client: r.Client, Clients: r.Clients, Namespace: r.Namespace, Backend: restartedBackend}
			for i := 0; i < len(testPhases)+1; i++ {
				reconcileOnce(t, restarted)
			}
			var current installv1.WaycloakInstallation
			if err := r.Client.Get(context.Background(), client.ObjectKey{Name: "waycloak"}, &current); err != nil {
				t.Fatal(err)
			}
			if !meta.IsStatusConditionTrue(current.Status.Conditions, "Ready") || current.Status.ReadyGeneration != 1 || len(restartedBackend.prepared) != 0 || restartedBackend.advanced[0] != stopped {
				t.Fatalf("did not resume and verify original intent: %+v", current.Status)
			}
		})
	}
}

func TestSupersedingIntentWaitsAndSuspendDoesNotAbandonTransition(t *testing.T) {
	r, b, installation := reconciliationFixture(t)
	reconcileOnce(t, r)
	var current installv1.WaycloakInstallation
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(installation), &current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Version = "v1.0.2-rc.5"
	current.Spec.Suspend = true
	current.Generation = 2
	if err := r.Client.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(testPhases)+1; i++ {
		reconcileOnce(t, r)
	}
	if len(b.prepared) != 1 || len(b.advanced) != len(testPhases)-1 {
		t.Fatal("new intent displaced an active transition")
	}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(installation), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != "Suspended" || meta.IsStatusConditionTrue(current.Status.Conditions, "Ready") {
		t.Fatal("new generation incorrectly reported ready")
	}
	current.Spec.Suspend = false
	current.Generation = 3
	if err := r.Client.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r)
	if len(b.prepared) != 2 || b.prepared[1].Version != "v1.0.2-rc.5" || !b.prepared[1].AdoptExisting {
		t.Fatal("queued intent was not prepared with established runtime ownership")
	}
}

func TestRecreatedInstallationCannotInheritJournalOrStatus(t *testing.T) {
	r, b, old := reconciliationFixture(t)
	reconcileOnce(t, r)
	if err := r.Client.Delete(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	replacement := old.DeepCopy()
	replacement.UID = "replacement-installation"
	replacement.ResourceVersion = ""
	if err := r.Client.Create(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if err := r.setStatus(context.Background(), old, func(current *installv1.WaycloakInstallation) { current.Status.Phase = "Ready" }); err == nil {
		t.Fatal("old reconciler wrote replacement status")
	}
	reconcileOnce(t, r)
	if len(b.advanced) != 0 || len(b.prepared) != 1 {
		t.Fatal("replacement stole active runtime")
	}
}

func TestObservedHealthLossWithdrawsReady(t *testing.T) {
	r, b, installation := reconciliationFixture(t)
	for i := 0; i < len(testPhases)+1; i++ {
		reconcileOnce(t, r)
	}
	b.verifyError = errors.New("node coverage lost")
	reconcileOnce(t, r)
	var current installv1.WaycloakInstallation
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(installation), &current); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionFalse(current.Status.Conditions, "Ready") {
		t.Fatal("readiness survived failed data-plane verification")
	}
}
