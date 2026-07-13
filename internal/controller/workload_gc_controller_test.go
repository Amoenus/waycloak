package controller

import (
	"context"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWorkloadDeletionQuarantine(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	deleting := metav1.NewTime(now.Add(-time.Second))
	workload := &wayv1.VPNWorkload{ObjectMeta: metav1.ObjectMeta{Name: "pod-test", Namespace: "apps", Finalizers: []string{contract.WorkloadFinalizer}, DeletionTimestamp: &deleting}, Status: wayv1.VPNWorkloadStatus{Allocation: wayv1.AllocationStatus{Address: "172.30.99.2"}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&wayv1.VPNWorkload{}).WithObjects(workload).Build()
	reconciler := &WorkloadGCReconciler{Client: c, Quarantine: 5 * time.Minute, Now: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "apps", Name: "pod-test"}}
	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("requeue=%s", result.RequeueAfter)
	}
	var held wayv1.VPNWorkload
	if err := c.Get(context.Background(), req.NamespacedName, &held); err != nil {
		t.Fatal(err)
	}
	if held.Status.Allocation.ReleasedAt == nil {
		t.Fatal("release time was not persisted")
	}
	if len(held.Finalizers) != 1 {
		t.Fatal("finalizer removed before quarantine elapsed")
	}
	reconciler.Now = func() time.Time { return now.Add(5 * time.Minute) }
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var released wayv1.VPNWorkload
	err = c.Get(context.Background(), req.NamespacedName, &released)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if err == nil && len(released.Finalizers) != 0 {
		t.Fatal("finalizer remained after quarantine elapsed")
	}
}
