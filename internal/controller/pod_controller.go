// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"fmt"
	"net/netip"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/admission"
	"github.com/Amoenus/waycloak/internal/allocation"
	"github.com/Amoenus/waycloak/internal/contract"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloads/finalizers,verbs=update

type PodReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if pod.Annotations[contract.GatewayAnnotation] == "" || pod.Annotations[contract.InjectionVersionAnnotation] != contract.InjectionVersion {
		return ctrl.Result{}, nil
	}
	gns, gname, err := admission.ParseGatewayReference(pod.Namespace, pod.Annotations[contract.GatewayAnnotation])
	if err != nil {
		return ctrl.Result{}, nil
	}
	var gateway wayv1.VPNGateway
	if err := r.Get(ctx, types.NamespacedName{Namespace: gns, Name: gname}, &gateway); err != nil {
		return ctrl.Result{}, err
	}
	name := contract.WorkloadName(string(pod.UID))
	key := types.NamespacedName{Namespace: pod.Namespace, Name: name}
	var workload wayv1.VPNWorkload
	if err := r.Get(ctx, key, &workload); apierrors.IsNotFound(err) {
		workload = wayv1.VPNWorkload{TypeMeta: metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNWorkload"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pod.Namespace, Finalizers: []string{contract.WorkloadFinalizer}}, Spec: wayv1.VPNWorkloadSpec{PodRef: wayv1.PodReference{Name: pod.Name, UID: pod.UID}, GatewayRef: wayv1.NamespacedNameReference{Namespace: gns, Name: gname}}}
		if err := ctrl.SetControllerReference(&pod, &workload, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, &workload); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(&pod, corev1.EventTypeNormal, "RegistrationCreated", "Created VPNWorkload %s", name)
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	if workload.Spec.PodRef.UID != pod.UID {
		return ctrl.Result{}, fmt.Errorf("VPNWorkload %s is bound to a different Pod UID", key)
	}
	if workload.Status.Allocation.Address == "" {
		var list wayv1.VPNWorkloadList
		if err := r.List(ctx, &list); err != nil {
			return ctrl.Result{}, err
		}
		used := map[netip.Addr]struct{}{}
		for i := range list.Items {
			w := &list.Items[i]
			if w.Spec.GatewayRef == workload.Spec.GatewayRef && w.Status.Allocation.Address != "" {
				if a, e := netip.ParseAddr(w.Status.Allocation.Address); e == nil {
					used[a] = struct{}{}
				}
			}
		}
		addr, err := allocation.Next(gateway.Spec.Overlay.CIDR, used)
		if err != nil {
			return ctrl.Result{}, err
		}
		workload.Status.ObservedGeneration = workload.Generation
		workload.Status.Allocation.Address = addr.String()
		workload.Status.Allocation.Generation = 1
		waystatus.Set(&workload.Status.Conditions, workload.Generation, waystatus.ConditionAccepted, metav1.ConditionTrue, waystatus.ReasonAccepted, "Registration is valid")
		waystatus.Set(&workload.Status.Conditions, workload.Generation, waystatus.ConditionAllocated, metav1.ConditionTrue, waystatus.ReasonAllocationPersisted, "Overlay address is durably persisted")
		waystatus.Set(&workload.Status.Conditions, workload.Generation, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonDataPlaneNotImplemented, "The Phase 1 control plane does not implement or observe a data plane")
		if err := r.Status().Update(ctx, &workload); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(&pod, corev1.EventTypeNormal, "AllocationPersisted", "Persisted overlay address %s", addr)
		return ctrl.Result{Requeue: true}, nil
	}
	cmName := contract.AllocationConfigMapName(pod.Namespace, pod.Name)
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: cmName}, &cm); apierrors.IsNotFound(err) {
		gatewayAddr, e := allocation.GatewayAddress(gateway.Spec.Overlay.CIDR)
		if e != nil {
			return ctrl.Result{}, e
		}
		cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: pod.Namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "waycloak"}}, Data: map[string]string{"version": contract.InjectionVersion, "podUID": string(pod.UID), "gateway": gns + "/" + gname, "address": workload.Status.Allocation.Address, "overlayCIDR": gateway.Spec.Overlay.CIDR, "gatewayAddress": gatewayAddr.String(), "allocationGeneration": fmt.Sprint(workload.Status.Allocation.Generation)}}
		if err := ctrl.SetControllerReference(&pod, &cm, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, &cm); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(&pod, corev1.EventTypeNormal, "AllocationPublished", "Created required UID-bound allocation ConfigMap")
	} else if err != nil {
		return ctrl.Result{}, err
	} else if !ownedByUID(&cm, pod.UID) {
		if err := r.Delete(ctx, &cm); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "StaleAllocationRemoved", "Removed allocation ConfigMap owned by a different Pod UID")
		return ctrl.Result{Requeue: true}, nil
	}
	previous := workload.Status
	previous.Conditions = append([]metav1.Condition(nil), workload.Status.Conditions...)
	waystatus.Set(&workload.Status.Conditions, workload.Generation, waystatus.ConditionAllocationPublished, metav1.ConditionTrue, waystatus.ReasonAllocationConfigMapReady, "The required UID-bound allocation ConfigMap exists")
	if apiequality.Semantic.DeepEqual(previous, workload.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Update(ctx, &workload); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func ownedByUID(obj metav1.Object, uid types.UID) bool {
	for _, o := range obj.GetOwnerReferences() {
		if o.Kind == "Pod" && o.UID == uid {
			return true
		}
	}
	return false
}

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&corev1.Pod{}).Owns(&wayv1.VPNWorkload{}).Owns(&corev1.ConfigMap{}).WithOptions(controller.Options{MaxConcurrentReconciles: 1}).Complete(r)
}
