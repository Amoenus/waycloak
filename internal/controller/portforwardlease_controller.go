// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/admission"
	"github.com/Amoenus/waycloak/internal/contract"
	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	appsv1 "k8s.io/api/apps/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=portforwardleases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=portforwardleases/status,verbs=get;update;patch

type PortForwardLeaseReconciler struct {
	client.Client
	Recorder           record.EventRecorder
	Now                func() time.Time
	Observer           waygateway.PortForwardObserver
	DeletionQuarantine time.Duration
}

const (
	providerDeletionQuarantine = 3 * time.Minute
	providerObservationPoll    = 2 * time.Second
)

func (r *PortForwardLeaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var lease wayv1.PortForwardLease
	if err := r.Get(ctx, req.NamespacedName, &lease); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !lease.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, &lease)
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
	if !controllerutil.ContainsFinalizer(&lease, contract.PortForwardLeaseFinalizer) {
		controllerutil.AddFinalizer(&lease, contract.PortForwardLeaseFinalizer)
		return ctrl.Result{}, r.Update(ctx, &lease)
	}
	if lease.Status.ProviderInternalPort == 0 {
		internalPort, allocationErr := r.allocateInternalPort(ctx, &lease, gatewayNamespace, gateway.Name)
		if allocationErr != nil {
			return ctrl.Result{}, allocationErr
		}
		lease.Status.ProviderInternalPort = int32(internalPort)
	}

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
	if r.Observer == nil {
		r.componentsPending(&lease, waystatus.ReasonProviderLeasePending, "No gateway provider-lease observer is configured")
		return ctrl.Result{RequeueAfter: providerObservationPoll}, r.updateStatus(ctx, &lease, previous)
	}
	observation, observationErr := r.observeProviderLease(ctx, &gateway, &lease)
	if observationErr != nil {
		if lease.Status.PublicPort > 0 && lease.Status.ExpiresAt != nil && r.now().Before(lease.Status.ExpiresAt.Time) {
			waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionProviderLeaseReady, metav1.ConditionTrue, waystatus.ReasonProviderLeaseObservedReady, "The last provider mapping observation remains current while gateway refresh is unavailable")
			r.downstreamPending(&lease)
		} else {
			r.componentsPending(&lease, waystatus.ReasonProviderLeaseObservationFailed, "Gateway provider-lease observation is unavailable")
		}
		return ctrl.Result{RequeueAfter: providerObservationPoll}, r.updateStatus(ctx, &lease, previous)
	}
	if !validProviderObservation(&lease, observation, r.now()) {
		reason := waystatus.ReasonProviderLeaseObservationFailed
		message := "Gateway provider-lease observation does not match this lease identity"
		if !observation.ExpiresAt.IsZero() && !r.now().Before(observation.ExpiresAt) {
			reason = waystatus.ReasonProviderLeaseExpired
			message = "Gateway provider-lease observation has expired"
		}
		r.componentsPending(&lease, reason, message)
		return ctrl.Result{RequeueAfter: providerObservationPoll}, r.updateStatus(ctx, &lease, previous)
	}
	previousPort := lease.Status.PublicPort
	lease.Status.PublicPort = int32(observation.PublicPort)
	issuedAt := metav1.NewTime(observation.IssuedAt)
	renewAfter := metav1.NewTime(observation.RenewAfter)
	expiresAt := metav1.NewTime(observation.ExpiresAt)
	lease.Status.IssuedAt = &issuedAt
	lease.Status.RenewAfter = &renewAfter
	lease.Status.ExpiresAt = &expiresAt
	if lease.Status.LeaseGeneration == 0 || previousPort != lease.Status.PublicPort {
		lease.Status.LeaseGeneration++
	}
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionProviderLeaseReady, metav1.ConditionTrue, waystatus.ReasonProviderLeaseObservedReady, "A current provider mapping is observed through the serving gateway Pod")
	if observation.GatewayRulesReady && observation.GatewayRulesGeneration == lease.Status.LeaseGeneration && lease.Status.Target != nil && observation.TargetAddress == lease.Status.Target.OverlayAddress && observation.TargetPort == uint16(lease.Status.Target.Port) {
		r.rulesObserved(&lease)
	} else {
		r.downstreamPending(&lease)
	}
	return ctrl.Result{RequeueAfter: providerObservationPoll}, r.updateStatus(ctx, &lease, previous)
}

func (r *PortForwardLeaseReconciler) observeProviderLease(ctx context.Context, gateway *wayv1.VPNGateway, lease *wayv1.PortForwardLease) (waygateway.PortForwardObservation, error) {
	var statefulSet appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Namespace: gateway.Namespace, Name: waygateway.ResourceName(gateway.Name)}, &statefulSet); err != nil {
		return waygateway.PortForwardObservation{}, fmt.Errorf("read serving gateway StatefulSet: %w", err)
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(gateway.Namespace), client.MatchingLabels(waygateway.SelectorLabels(gateway))); err != nil {
		return waygateway.PortForwardObservation{}, err
	}
	var selected *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := metav1.GetControllerOf(pod)
		if !pod.DeletionTimestamp.IsZero() || pod.Status.PodIP == "" || owner == nil || owner.APIVersion != appsv1.SchemeGroupVersion.String() || owner.Kind != "StatefulSet" || owner.UID != statefulSet.UID {
			continue
		}
		if selected == nil || pod.CreationTimestamp.After(selected.CreationTimestamp.Time) {
			selected = pod
		}
	}
	if selected == nil {
		return waygateway.PortForwardObservation{}, errors.New("serving gateway Pod is unavailable")
	}
	return r.Observer.ObserveLease(ctx, selected.Status.PodIP, string(lease.UID))
}

func validProviderObservation(lease *wayv1.PortForwardLease, observation waygateway.PortForwardObservation, now time.Time) bool {
	if !observation.Ready || observation.Identity != string(lease.UID) || observation.InternalPort != uint16(lease.Status.ProviderInternalPort) || observation.PublicPort == 0 || observation.IssuedAt.IsZero() || observation.RenewAfter.IsZero() || observation.ExpiresAt.IsZero() || !observation.IssuedAt.Before(observation.ExpiresAt) || !observation.RenewAfter.Before(observation.ExpiresAt) || !now.Before(observation.ExpiresAt) {
		return false
	}
	wanted := make([]string, 0, len(lease.Spec.Protocols))
	for _, protocol := range lease.Spec.Protocols {
		wanted = append(wanted, string(protocol))
	}
	got := make([]string, 0, len(observation.Protocols))
	for _, protocol := range observation.Protocols {
		got = append(got, string(protocol))
	}
	slices.Sort(wanted)
	slices.Sort(got)
	return slices.Equal(wanted, got)
}

func (r *PortForwardLeaseReconciler) allocateInternalPort(ctx context.Context, lease *wayv1.PortForwardLease, gatewayNamespace, gatewayName string) (uint16, error) {
	var leases wayv1.PortForwardLeaseList
	if err := r.List(ctx, &leases); err != nil {
		return 0, err
	}
	used := make(map[uint16]struct{}, len(leases.Items))
	for i := range leases.Items {
		candidate := &leases.Items[i]
		namespace := candidate.Spec.GatewayRef.Namespace
		if namespace == "" {
			namespace = candidate.Namespace
		}
		if candidate.UID == lease.UID || namespace != gatewayNamespace || candidate.Spec.GatewayRef.Name != gatewayName || candidate.Status.ProviderInternalPort < 1 || candidate.Status.ProviderInternalPort > 65535 {
			continue
		}
		used[uint16(candidate.Status.ProviderInternalPort)] = struct{}{}
	}
	for port := uint32(1); port <= 65535; port++ {
		if _, exists := used[uint16(port)]; !exists {
			return uint16(port), nil
		}
	}
	return 0, errors.New("provider internal-port allocation is exhausted")
}

func (r *PortForwardLeaseReconciler) reconcileDeletion(ctx context.Context, lease *wayv1.PortForwardLease) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(lease, contract.PortForwardLeaseFinalizer) {
		return ctrl.Result{}, nil
	}
	deadline := lease.DeletionTimestamp.Time.Add(r.deletionQuarantine())
	if lease.Status.ExpiresAt != nil && lease.Status.ExpiresAt.Time.After(deadline) {
		deadline = lease.Status.ExpiresAt.Time
	}
	if remaining := deadline.Sub(r.now()); remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}, nil
	}
	controllerutil.RemoveFinalizer(lease, contract.PortForwardLeaseFinalizer)
	return ctrl.Result{}, r.Update(ctx, lease)
}

func (r *PortForwardLeaseReconciler) deletionQuarantine() time.Duration {
	if r.DeletionQuarantine > 0 {
		return r.DeletionQuarantine
	}
	return providerDeletionQuarantine
}

func (r *PortForwardLeaseReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
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

func (r *PortForwardLeaseReconciler) downstreamPending(lease *wayv1.PortForwardLease) {
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionGatewayRulesReady, metav1.ConditionFalse, waystatus.ReasonGatewayRulesPending, "Gateway DNAT for the observed lease generation is not ready")
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionDelivered, metav1.ConditionFalse, waystatus.ReasonDeliveryPending, "The current lease generation has not been delivered")
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonLeaseComponentsNotReady, "Gateway rules, target, and delivery must all be observed ready")
}

func (r *PortForwardLeaseReconciler) rulesObserved(lease *wayv1.PortForwardLease) {
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionGatewayRulesReady, metav1.ConditionTrue, waystatus.ReasonGatewayRulesObservedReady, "Gateway TCP/UDP DNAT is observed for the current lease generation and UID-bound target")
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionDelivered, metav1.ConditionFalse, waystatus.ReasonDeliveryPending, "The current lease generation has not been delivered")
	waystatus.Set(&lease.Status.Conditions, lease.Generation, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonLeaseComponentsNotReady, "Lease delivery must be observed before the lease is ready")
}

func (r *PortForwardLeaseReconciler) updateStatus(ctx context.Context, lease *wayv1.PortForwardLease, previous *wayv1.PortForwardLeaseStatus) error {
	if apiequality.Semantic.DeepEqual(*previous, lease.Status) {
		return nil
	}
	if err := r.Status().Update(ctx, lease); err != nil {
		return err
	}
	if r.Recorder != nil {
		for _, conditionType := range []string{waystatus.ConditionAccepted, waystatus.ConditionTargetReady, waystatus.ConditionProviderLeaseReady, waystatus.ConditionGatewayRulesReady, waystatus.ConditionReady} {
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
