// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	installv1 "github.com/Amoenus/waycloak/api/installation/v1alpha1"
	"github.com/Amoenus/waycloak/internal/waycloakctl"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const journalName = "waycloak-installation-active"
const ownerName = "waycloak-installation-owner"
const ownerKey = "installation.waycloak.io/owner-uid"
const phaseKey = "installation.waycloak.io/phase"

type journal struct {
	InstallationUID types.UID                     `json:"installationUID"`
	Generation      int64                         `json:"generation"`
	IntentDigest    string                        `json:"intentDigest"`
	Snapshot        waycloakctl.NativeInstallPlan `json:"snapshot"`
}

// NativeBackend's observations are the authority for advancing a phase. It is
// replaceable in reconciliation tests without relaxing the journal protocol.
type NativeBackend interface {
	Prepare(context.Context, installv1.WaycloakInstallationSpec) (waycloakctl.NativeInstallPlan, error)
	Advance(context.Context, types.UID, waycloakctl.NativeInstallPlan, string) (string, error)
	Verify(context.Context, waycloakctl.NativeInstallPlan) error
}

type Reconciler struct {
	Client    client.Client
	Clients   *waycloakctl.Clients
	Namespace string
	Backend   NativeBackend
}

func (r *Reconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&installv1.WaycloakInstallation{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).Complete(r)
}

func intentDigest(spec installv1.WaycloakInstallationSpec) string {
	// Adoption and suspension are controls, not target runtime configuration.
	spec.AdoptExisting, spec.Suspend = false, false
	encoded, _ := json.Marshal(spec)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *Reconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	if request.Name != "waycloak" || request.Namespace != "" {
		return ctrl.Result{}, nil
	}
	var installation installv1.WaycloakInstallation
	if err := r.Client.Get(ctx, request.NamespacedName, &installation); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !installation.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	result, err := r.reconcile(ctx, &installation)
	if err != nil {
		message := err.Error()
		if len(message) > 1024 {
			message = message[:1024]
		}
		statusErr := r.setStatus(ctx, &installation, func(current *installv1.WaycloakInstallation) {
			current.Status.ObservedGeneration = current.Generation
			meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, ObservedGeneration: current.Generation, Reason: "ReconciliationFailed", Message: message})
		})
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		// Retry observable failures without discarding the active transaction.
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	return result, nil
}

func (r *Reconciler) reconcile(ctx context.Context, installation *installv1.WaycloakInstallation) (ctrl.Result, error) {
	intent := intentDigest(installation.Spec)
	snapshot, record, err := r.loadJournal(ctx, installation)
	if err != nil {
		return ctrl.Result{}, err
	}
	if record != nil {
		owned, err := r.runtimeOwned(ctx, installation)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !owned {
			return ctrl.Result{}, errors.New("active installation runtime ownership is missing")
		}
	}
	if record != nil && record.Annotations[phaseKey] == waycloakctl.NativePhaseComplete && snapshot.IntentDigest == intent {
		if err := r.Backend.Verify(ctx, snapshot.Snapshot); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.ready(ctx, installation, snapshot)
	}
	if (record == nil || record.Annotations[phaseKey] == waycloakctl.NativePhaseComplete) && installation.Spec.Suspend {
		return ctrl.Result{}, r.setStatus(ctx, installation, func(current *installv1.WaycloakInstallation) {
			current.Status.Phase = "Suspended"
			current.Status.ObservedGeneration = current.Generation
			meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, ObservedGeneration: current.Generation, Reason: "Suspended", Message: "New installation intent is suspended; existing runtime is retained"})
		})
	}
	if record == nil || record.Annotations[phaseKey] == waycloakctl.NativePhaseComplete {
		owned, err := r.runtimeOwned(ctx, installation)
		if err != nil {
			return ctrl.Result{}, err
		}
		spec := installation.Spec
		if owned {
			spec.AdoptExisting = true
		}
		prepared, err := r.Backend.Prepare(ctx, spec)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.claimRuntime(ctx, installation); err != nil {
			return ctrl.Result{}, err
		}
		next := journal{InstallationUID: installation.UID, Generation: installation.Generation, IntentDigest: intent, Snapshot: prepared}
		data, err := json.Marshal(next)
		if err != nil {
			return ctrl.Result{}, err
		}
		if len(data) > 512<<10 {
			return ctrl.Result{}, errors.New("installation journal exceeds size limit")
		}
		if record != nil {
			uid := record.UID
			if err := r.Clients.Kubernetes.CoreV1().ConfigMaps(r.Namespace).Delete(ctx, record.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
				return ctrl.Result{}, err
			}
		}
		immutable := true
		_, err = r.Clients.Kubernetes.CoreV1().ConfigMaps(r.Namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: journalName, Namespace: r.Namespace, Annotations: map[string]string{ownerKey: string(installation.UID), phaseKey: waycloakctl.NativePhaseHold}}, Immutable: &immutable, Data: map[string]string{"journal.json": string(data)}}, metav1.CreateOptions{})
		// No executable mutation has happened yet. A crash before creation can
		// safely reobserve and prepare from the still-installed source.
		return ctrl.Result{RequeueAfter: time.Millisecond}, err
	}
	phase := record.Annotations[phaseKey]
	if err := r.setStatus(ctx, installation, func(current *installv1.WaycloakInstallation) {
		current.Status.ObservedGeneration = current.Generation
		current.Status.ActiveGeneration = snapshot.Generation
		current.Status.ActivePlanID = snapshot.Snapshot.Plan.PlanID
		current.Status.Phase = phase
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, ObservedGeneration: current.Generation, Reason: "Reconciling", Message: "Reconciling immutable installation intent; newer generations wait for this transition"})
	}); err != nil {
		return ctrl.Result{}, err
	}
	next, err := r.Backend.Advance(ctx, installation.UID, snapshot.Snapshot, phase)
	if err != nil {
		return ctrl.Result{}, err
	}
	if next != phase {
		record = record.DeepCopy()
		record.Annotations[phaseKey] = next
		if _, err := r.Clients.Kubernetes.CoreV1().ConfigMaps(r.Namespace).Update(ctx, record, metav1.UpdateOptions{}); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

func (r *Reconciler) loadJournal(ctx context.Context, installation *installv1.WaycloakInstallation) (journal, *corev1.ConfigMap, error) {
	var snapshot journal
	record, err := r.Clients.Kubernetes.CoreV1().ConfigMaps(r.Namespace).Get(ctx, journalName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return snapshot, nil, nil
	}
	if err != nil {
		return snapshot, nil, err
	}
	if record.Immutable == nil || !*record.Immutable || record.Annotations[ownerKey] != string(installation.UID) || len(record.Data) != 1 || len(record.Data["journal.json"]) > 512<<10 {
		return snapshot, nil, errors.New("installation journal is foreign or malformed")
	}
	if err := json.Unmarshal([]byte(record.Data["journal.json"]), &snapshot); err != nil {
		return snapshot, nil, errors.New("installation journal cannot be decoded")
	}
	if snapshot.InstallationUID != installation.UID || snapshot.Generation < 1 || !regexpDigest.MatchString(snapshot.IntentDigest) || snapshot.Snapshot.Plan.Namespace != installation.Spec.Namespace || snapshot.Snapshot.Plan.Release != installation.Spec.Release {
		return snapshot, nil, errors.New("installation journal does not match this installation identity")
	}
	return snapshot, record, nil
}

func (r *Reconciler) runtimeOwned(ctx context.Context, installation *installv1.WaycloakInstallation) (bool, error) {
	owner, err := r.Clients.Kubernetes.CoreV1().ConfigMaps(installation.Spec.Namespace).Get(ctx, ownerName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if owner.Immutable == nil || !*owner.Immutable || owner.Data["installationUID"] != string(installation.UID) || owner.Data["release"] != installation.Spec.Release {
		return false, errors.New("runtime belongs to another installation UID")
	}
	return true, nil
}

func (r *Reconciler) claimRuntime(ctx context.Context, installation *installv1.WaycloakInstallation) error {
	owned, err := r.runtimeOwned(ctx, installation)
	if err != nil || owned {
		return err
	}
	immutable := true
	_, err = r.Clients.Kubernetes.CoreV1().ConfigMaps(installation.Spec.Namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ownerName, Namespace: installation.Spec.Namespace}, Immutable: &immutable, Data: map[string]string{"installationUID": string(installation.UID), "release": installation.Spec.Release}}, metav1.CreateOptions{})
	return err
}

func (r *Reconciler) ready(ctx context.Context, installation *installv1.WaycloakInstallation, snapshot journal) error {
	return r.setStatus(ctx, installation, func(current *installv1.WaycloakInstallation) {
		// A late response for an older generation must not report the new intent ready.
		if intentDigest(current.Spec) != snapshot.IntentDigest {
			return
		}
		current.Status.ObservedGeneration, current.Status.ReadyGeneration = current.Generation, current.Generation
		current.Status.ActiveGeneration, current.Status.ActivePlanID = 0, ""
		current.Status.AppliedVersion = snapshot.Snapshot.Plan.Target.Version
		current.Status.AppliedManifestDigest = snapshot.Snapshot.Plan.Target.ManifestDigest
		current.Status.AppliedIntentDigest = snapshot.IntentDigest
		current.Status.Phase = "Ready"
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: current.Generation, Reason: "Reconciled", Message: "Exact runtime, node coverage, trust, gateways, and live bindings are observed ready"})
	})
}

func (r *Reconciler) setStatus(ctx context.Context, expected *installv1.WaycloakInstallation, change func(*installv1.WaycloakInstallation)) error {
	var current installv1.WaycloakInstallation
	if err := r.Client.Get(ctx, types.NamespacedName{Name: expected.Name}, &current); err != nil {
		return err
	}
	if current.UID != expected.UID {
		return errors.New("installation UID changed during reconciliation")
	}
	before := current.DeepCopy()
	change(&current)
	if reflect.DeepEqual(current.Status, before.Status) {
		return nil
	}
	if err := r.Client.Status().Patch(ctx, &current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("record installation status: %w", err)
	}
	return nil
}
