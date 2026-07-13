// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"fmt"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/admission"
	"github.com/Amoenus/waycloak/internal/contract"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=portforwardleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=portforwardleases/status,verbs=get;update;patch

type PortForwardLeaseReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

func (r *PortForwardLeaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var lease wayv1.PortForwardLease
	if err := r.Get(ctx, req.NamespacedName, &lease); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	previous := lease.Status.DeepCopy()
	lease.Status.ObservedGeneration = lease.Generation
	lease.Status.Target = nil

	gatewayNamespace := lease.Spec.GatewayRef.Namespace
	if gatewayNamespace == "" {
		gatewayNamespace = lease.Namespace
	}
	if lease.Spec.GatewayRef.Name == "" {
		r.reject(&lease, waystatus.ReasonGatewayNotFound, "spec.gatewayRef.name is required")
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	var gateway wayv1.VPNGateway
	if err := r.Get(ctx, types.NamespacedName{Namespace: gatewayNamespace, Name: lease.Spec.GatewayRef.Name}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			r.reject(&lease, waystatus.ReasonGatewayNotFound, fmt.Sprintf("VPNGateway %s/%s does not exist", gatewayNamespace, lease.Spec.GatewayRef.Name))
			return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
		}
		return ctrl.Result{}, err
	}
	var namespace corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: lease.Namespace}, &namespace); err != nil {
		return ctrl.Result{}, err
	}
	namespaceSelector, err := metav1.LabelSelectorAsSelector(&gateway.Spec.WorkloadAccess.NamespaceSelector)
	if err != nil || !namespaceSelector.Matches(labels.Set(namespace.Labels)) {
		r.reject(&lease, waystatus.ReasonUnauthorizedGateway, fmt.Sprintf("namespace %q is not authorized by VPNGateway %s/%s", lease.Namespace, gatewayNamespace, gateway.Name))
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	if len(lease.Spec.Target.PodSelector.MatchLabels) == 0 && len(lease.Spec.Target.PodSelector.MatchExpressions) == 0 {
		r.reject(&lease, waystatus.ReasonInvalidTargetSelector, "spec.target.podSelector must not be empty")
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	targetSelector, err := metav1.LabelSelectorAsSelector(&lease.Spec.Target.PodSelector)
	if err != nil {
		r.reject(&lease, waystatus.ReasonInvalidTargetSelector, "spec.target.podSelector is invalid")
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionAccepted, metav1.ConditionTrue, waystatus.ReasonLeaseAccepted, "Lease intent is valid and gateway use is authorized")

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(lease.Namespace), client.MatchingLabelsSelector{Selector: targetSelector}); err != nil {
		return ctrl.Result{}, err
	}
	ready := make([]*corev1.Pod, 0, 1)
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp.IsZero() && podReady(pod) {
			ready = append(ready, pod)
		}
	}
	if len(ready) == 0 {
		r.targetPending(&lease, waystatus.ReasonTargetNotFound, "No Ready target Pod matches the selector")
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	if len(ready) != 1 {
		r.targetPending(&lease, waystatus.ReasonTargetAmbiguous, fmt.Sprintf("Selector matches %d Ready target Pods; exactly one is required", len(ready)))
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	target := ready[0]
	selectedNamespace, selectedName, err := admission.ParseGatewayReference(target.Namespace, target.Annotations[contract.GatewayAnnotation])
	if err != nil || selectedNamespace != gatewayNamespace || selectedName != gateway.Name {
		r.targetPending(&lease, waystatus.ReasonTargetGatewayMismatch, "Ready target Pod is not protected by the selected gateway")
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	workloadName := contract.WorkloadName(string(target.UID))
	var workload wayv1.VPNWorkload
	if err := r.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: workloadName}, &workload); err != nil {
		if apierrors.IsNotFound(err) {
			r.targetPending(&lease, waystatus.ReasonTargetRegistrationPending, "Target VPNWorkload registration is not observed yet")
			return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
		}
		return ctrl.Result{}, err
	}
	if workload.Spec.PodRef.UID != target.UID || workload.Spec.GatewayRef.Namespace != gatewayNamespace || workload.Spec.GatewayRef.Name != gateway.Name || workload.Status.Allocation.Address == "" {
		r.targetPending(&lease, waystatus.ReasonTargetRegistrationPending, "Target UID-bound overlay allocation is not ready")
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	lease.Status.Target = &wayv1.PortForwardTargetStatus{PodRef: wayv1.PodReference{Name: target.Name, UID: target.UID}, WorkloadRef: wayv1.NamespacedNameReference{Namespace: target.Namespace, Name: workload.Name}, OverlayAddress: workload.Status.Allocation.Address, Port: lease.Spec.Target.Port}
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionTargetReady, metav1.ConditionTrue, waystatus.ReasonTargetObservedReady, "Exactly one Ready UID-bound target and overlay allocation are observed")

	if !gateway.Spec.PortForwarding.Enabled {
		waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionAccepted, metav1.ConditionFalse, waystatus.ReasonPortForwardUnsupported, "The selected gateway has port forwarding disabled")
		r.componentsPending(&lease, waystatus.ReasonPortForwardUnsupported, "Provider lease acquisition is unavailable")
		return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
	}
	r.componentsPending(&lease, waystatus.ReasonProviderLeasePending, "No provider lease has been observed; NAT-PMP is not implemented in this slice")
	return ctrl.Result{}, r.updateStatus(ctx, &lease, previous)
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (r *PortForwardLeaseReconciler) reject(lease *wayv1.PortForwardLease, reason, message string) {
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionAccepted, metav1.ConditionFalse, reason, message)
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionTargetReady, metav1.ConditionFalse, reason, message)
	r.componentsPending(lease, reason, message)
}

func (r *PortForwardLeaseReconciler) targetPending(lease *wayv1.PortForwardLease, reason, message string) {
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionTargetReady, metav1.ConditionFalse, reason, message)
	r.componentsPending(lease, reason, message)
}

func (r *PortForwardLeaseReconciler) componentsPending(lease *wayv1.PortForwardLease, reason, message string) {
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionProviderLeaseReady, metav1.ConditionFalse, reason, message)
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionGatewayRulesReady, metav1.ConditionFalse, waystatus.ReasonGatewayRulesPending, "Gateway DNAT for the observed lease generation is not ready")
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionDelivered, metav1.ConditionFalse, waystatus.ReasonDeliveryPending, "The current lease generation has not been delivered")
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonLeaseComponentsNotReady, "Provider lease, gateway rules, target, and delivery must all be observed ready")
}

func (r *PortForwardLeaseReconciler) updateStatus(ctx context.Context, lease *wayv1.PortForwardLease, previous *wayv1.PortForwardLeaseStatus) error {
	if apiequality.Semantic.DeepEqual(*previous, lease.Status) {
		return nil
	}
	if err := r.Status().Update(ctx, lease); err != nil {
		return err
	}
	if r.Recorder != nil {
		for _, conditionType := range []string{waystatus.ConditionAccepted, waystatus.ConditionTargetReady, waystatus.ConditionProviderLeaseReady, waystatus.ConditionReady} {
			before := apiMeta.FindStatusCondition(previous.Conditions, conditionType)
			after := apiMeta.FindStatusCondition(lease.Status.Conditions, conditionType)
			if after == nil || before != nil && before.Status == after.Status && before.Reason == after.Reason {
				continue
			}
			eventType := corev1.EventTypeNormal
			if after.Status == metav1.ConditionFalse {
				eventType = corev1.EventTypeWarning
			}
			r.Recorder.Event(lease, eventType, after.Reason, after.Message)
		}
	}
	return nil
}

func (r *PortForwardLeaseReconciler) enqueueNamespaceLeases(ctx context.Context, object client.Object) []reconcile.Request {
	var leases wayv1.PortForwardLeaseList
	if err := r.List(ctx, &leases, client.InNamespace(object.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(leases.Items))
	for i := range leases.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: leases.Items[i].Namespace, Name: leases.Items[i].Name}})
	}
	return requests
}

func (r *PortForwardLeaseReconciler) enqueueGatewayLeases(ctx context.Context, object client.Object) []reconcile.Request {
	var leases wayv1.PortForwardLeaseList
	if err := r.List(ctx, &leases); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for i := range leases.Items {
		lease := &leases.Items[i]
		namespace := lease.Spec.GatewayRef.Namespace
		if namespace == "" {
			namespace = lease.Namespace
		}
		if namespace == object.GetNamespace() && lease.Spec.GatewayRef.Name == object.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}})
		}
	}
	return requests
}

func (r *PortForwardLeaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wayv1.PortForwardLease{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.enqueueNamespaceLeases)).
		Watches(&wayv1.VPNWorkload{}, handler.EnqueueRequestsFromMapFunc(r.enqueueNamespaceLeases)).
		Watches(&wayv1.VPNGateway{}, handler.EnqueueRequestsFromMapFunc(r.enqueueGatewayLeases)).
		Complete(r)
}
