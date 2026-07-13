package controller

import (
	"context"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type WorkloadGCReconciler struct {
	client.Client
	Quarantine time.Duration
	Now        func() time.Time
}

func (r *WorkloadGCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var w wayv1.VPNWorkload
	if err := r.Get(ctx, req.NamespacedName, &w); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if w.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(&w, contract.WorkloadFinalizer) {
		return ctrl.Result{}, nil
	}
	now := r.Now()
	if w.Status.Allocation.ReleasedAt == nil {
		t := metav1.NewTime(now)
		w.Status.Allocation.ReleasedAt = &t
		if err := r.Status().Update(ctx, &w); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.Quarantine}, nil
	}
	deadline := w.Status.Allocation.ReleasedAt.Add(r.Quarantine)
	if now.Before(deadline) {
		return ctrl.Result{RequeueAfter: deadline.Sub(now)}, nil
	}
	controllerutil.RemoveFinalizer(&w, contract.WorkloadFinalizer)
	return ctrl.Result{}, r.Update(ctx, &w)
}
func (r *WorkloadGCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Quarantine == 0 {
		r.Quarantine = 5 * time.Minute
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	return ctrl.NewControllerManagedBy(mgr).For(&wayv1.VPNWorkload{}).Complete(r)
}
