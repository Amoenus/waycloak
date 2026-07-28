// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package scheduling

import (
	"context"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type NodeCapabilityReconciler struct {
	client.Client
	Now       func() time.Time
	Freshness time.Duration
}

func (r *NodeCapabilityReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	node := &corev1.Node{}
	if err := r.Get(ctx, request.NamespacedName, node); err != nil {
		return ctrl.Result{}, IgnoreNotFound(err)
	}
	if node.Labels[CoreReadyLabel] != "true" {
		return ctrl.Result{}, nil
	}
	now := r.now()
	freshness := r.freshness()
	if Stale(node, now, freshness) {
		return ctrl.Result{}, (Publisher{Client: r.Client}).Withdraw(ctx, node.Name)
	}
	epoch, _ := strconv.ParseInt(node.Labels[CapabilityEpochLabel], 10, 64)
	return ctrl.Result{RequeueAfter: freshness - now.Sub(time.Unix(epoch, 0).UTC())}, nil
}

func (r *NodeCapabilityReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&corev1.Node{}).Complete(r)
}

func (r *NodeCapabilityReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *NodeCapabilityReconciler) freshness() time.Duration {
	if r.Freshness > 0 {
		return r.Freshness
	}
	return DefaultFreshness
}
