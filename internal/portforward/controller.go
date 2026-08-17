// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	wayconditions "github.com/Amoenus/waycloak/internal/conditions"
	"github.com/Amoenus/waycloak/internal/reference"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	LeaseBackendIndex           = "networking.waycloak.io/lease-backend-service"
	DefaultObservationFreshness = 20 * time.Second
	DefaultCleanupTimeout       = 10 * time.Minute
)

var leaseConditionOrder = []string{
	wayv1.ConditionAccepted,
	wayv1.ConditionResolvedRefs,
	wayv1.ConditionProgrammed,
	wayv1.ConditionReady,
	wayv1.ConditionGatewayRulesReady,
	wayv1.ConditionDelivered,
	wayv1.ConditionAcknowledged,
}

type PortForwardLeaseReconciler struct {
	client.Client
	APIReader            client.Reader
	Authorizer           reference.GatewayResolver
	Runtime              Runtime
	Allocator            ProviderPortAllocator
	Now                  func() time.Time
	ObservationFreshness time.Duration
	CleanupTimeout       time.Duration
}

func (r *PortForwardLeaseReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	lease := &wayv1.PortForwardLease{}
	if err := r.Get(ctx, request.NamespacedName, lease); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !lease.DeletionTimestamp.IsZero() {
		return r.cleanup(ctx, lease)
	}

	evaluation := r.evaluate(ctx, lease)
	if evaluation.runtimeEligible && (evaluation.hasSelected || lease.Status.ActiveEndpoint != nil) && r.Runtime != nil && !containsString(lease.Finalizers, ProviderCleanupFinalizer) {
		before := lease.DeepCopy()
		lease.Finalizers = append(lease.Finalizers, ProviderCleanupFinalizer)
		if err := r.Patch(ctx, lease, client.MergeFrom(before), client.FieldOwner(wayv1.FieldManagerLeaseController)); err != nil {
			return ctrl.Result{}, fmt.Errorf("install provider cleanup finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	desired, requeueAfter := r.runtimeStatus(ctx, lease, evaluation)
	if !reflect.DeepEqual(lease.Status, desired) {
		if err := r.applyStatus(ctx, lease, desired); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

type leaseEvaluation struct {
	states                          map[string]wayconditions.State
	gateway                         *wayv1.VPNGateway
	resolution                      Resolution
	selected                        Candidate
	hasSelected                     bool
	runtimeEligible                 bool
	gatewayReady                    bool
	providerAssignedApplicationPort bool
}

func (r *PortForwardLeaseReconciler) evaluate(ctx context.Context, lease *wayv1.PortForwardLease) leaseEvaluation {
	evaluation := leaseEvaluation{states: pendingLeaseStates(lease.Spec.ApplicationAdapterRef == nil)}
	authorizer := r.Authorizer
	if authorizer == nil {
		authorizer = reference.GatewayAuthorizer{Reader: r.reader()}
	}
	resolution, err := authorizer.ResolveGateway(ctx, lease.Namespace, lease.Spec.GatewayRef)
	switch {
	case err != nil:
		unknownLeaseStates(evaluation.states, "Gateway reference observation is unavailable")
		return evaluation
	case !resolution.Permitted:
		evaluation.states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonRefNotPermitted, "Gateway reference is not permitted")
		return evaluation
	case resolution.Gateway == nil:
		evaluation.states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonRefNotFound, "Gateway reference was not found")
		return evaluation
	}
	gateway := resolution.Gateway
	evaluation.gateway = gateway
	evaluation.runtimeEligible = lease.Status.ActiveEndpoint != nil
	if !gatewayAccepted(gateway) {
		evaluation.states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedClass, "Gateway is not accepted")
		evaluation.states[wayv1.ConditionResolvedRefs] = wayconditions.True(wayv1.ReasonResolvedRefs, "Gateway reference is resolved")
		return evaluation
	}
	if !hasGatewayFeature(gateway, wayv1.FeaturePortForwardSingleActive) {
		evaluation.states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedFeature, "Gateway does not advertise SingleActive port forwarding")
		evaluation.states[wayv1.ConditionResolvedRefs] = wayconditions.True(wayv1.ReasonResolvedRefs, "Gateway reference is resolved")
		return evaluation
	}
	evaluation.states[wayv1.ConditionAccepted] = wayconditions.True(wayv1.ReasonAccepted, "Lease intent is accepted")
	evaluation.gatewayReady = gatewayReady(gateway)
	evaluation.runtimeEligible = evaluation.runtimeEligible || evaluation.gatewayReady

	backend, backendErr := (Resolver{Reader: r.reader()}).Resolve(ctx, lease, gateway)
	if backendErr != nil {
		var resolutionErr *ResolutionError
		if errorsAsResolution(backendErr, &resolutionErr) && resolutionErr.Kind == ResolutionUnavailable {
			unknownLeaseStates(evaluation.states, "Backend observation is unavailable")
			evaluation.states[wayv1.ConditionAccepted] = wayconditions.True(wayv1.ReasonAccepted, "Lease intent is accepted")
		} else {
			evaluation.states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonRefNotFound, "Backend Service or endpoint is unresolved")
		}
		return evaluation
	}
	evaluation.resolution = backend
	if lease.Spec.ApplicationAdapterRef != nil {
		if !hasGatewayFeature(gateway, wayv1.FeatureWorkloadAdapter) {
			evaluation.states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedFeature, "Gateway does not advertise workload adapters")
			return evaluation
		}
		adapter := &wayv1.WorkloadAdapter{}
		if err := r.reader().Get(ctx, client.ObjectKey{Namespace: lease.Namespace, Name: string(lease.Spec.ApplicationAdapterRef.Name)}, adapter); err != nil ||
			!wayconditions.CurrentTrue(adapter.Status.Conditions, wayv1.ConditionReady, adapter.Status.ObservedGeneration, adapter.Generation) {
			evaluation.states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonRefNotFound, "Application adapter reference is unresolved")
			return evaluation
		}
		for _, feature := range adapter.Spec.SupportedFeatures {
			if feature == ProviderAssignedApplicationPortFeature {
				evaluation.providerAssignedApplicationPort = true
				break
			}
		}
	}
	selected, ok := Select(backend.Candidates, lease.Status.ActiveEndpoint)
	if !ok {
		evaluation.states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonRefNotFound, "No eligible SingleActive Service endpoint is available")
		return evaluation
	}
	evaluation.states[wayv1.ConditionResolvedRefs] = wayconditions.True(wayv1.ReasonResolvedRefs, "Gateway and exact Service endpoint are resolved")
	evaluation.selected, evaluation.hasSelected = selected, true
	return evaluation
}

func (r *PortForwardLeaseReconciler) runtimeStatus(ctx context.Context, lease *wayv1.PortForwardLease, evaluation leaseEvaluation) (wayv1.PortForwardLeaseStatus, time.Duration) {
	status := wayv1.PortForwardLeaseStatus{ObservedGeneration: lease.Generation, HandoffGeneration: lease.Status.HandoffGeneration}
	states := evaluation.states
	if !evaluation.runtimeEligible || r.Runtime == nil {
		if evaluation.runtimeEligible && r.Runtime == nil {
			unknownLeaseStates(states, "Gateway runtime observation is unavailable")
			states[wayv1.ConditionAccepted] = wayconditions.True(wayv1.ReasonAccepted, "Lease intent is accepted")
			states[wayv1.ConditionResolvedRefs] = wayconditions.True(wayv1.ReasonResolvedRefs, "Gateway and exact Service endpoint are resolved")
		}
		status.ActiveEndpoint = copyEndpoint(lease.Status.ActiveEndpoint)
		status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
		return status, r.observationFreshness()
	}

	current := lease.Status.ActiveEndpoint
	if current == nil && !evaluation.hasSelected {
		status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
		return status, r.observationFreshness()
	}
	if current != nil && (!evaluation.gatewayReady || current.Phase == wayv1.EndpointPhaseDraining || !evaluation.hasSelected || current.ServiceUID != evaluation.selected.ServiceUID || current.PodUID != evaluation.selected.PodUID) {
		return r.drainStatus(ctx, lease, evaluation)
	}
	if current == nil {
		status.HandoffGeneration++
		status.ActiveEndpoint = endpointFor(evaluation.selected, wayv1.EndpointPhaseSelecting)
		// Persist the successor identity before asking the external runtime to
		// acquire, program, or deliver it. If the later status write conflicts,
		// the next reconcile therefore retries the same generation instead of
		// regressing behind an already-applied adapter generation.
		status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
		return status, time.Millisecond
	}
	status.ActiveEndpoint = endpointFor(evaluation.selected, current.Phase)
	if status.ActiveEndpoint.Phase != wayv1.EndpointPhaseActive {
		status.ActiveEndpoint.Phase = wayv1.EndpointPhaseSelecting
	}
	intent := IntentFor(lease, evaluation.gateway, evaluation.selected, status.HandoffGeneration)
	if evaluation.providerAssignedApplicationPort {
		intent.ApplicationPortMode = ApplicationPortProviderAssigned
	}
	allocator := r.Allocator
	if allocator.Client == nil {
		allocator = ProviderPortAllocator{Client: r.Client, Now: r.Now}
	}
	providerPort, allocationErr := allocator.Reserve(ctx, lease, evaluation.gateway)
	if allocationErr != nil {
		unknownRuntimeStates(states)
		status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
		return status, r.observationFreshness()
	}
	intent.ProviderInternalPort = providerPort
	observation, err := r.Runtime.Reconcile(ctx, evaluation.gateway, intent)
	if err != nil || !r.currentObservation(observation, intent) {
		unknownRuntimeStates(states)
		status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
		return status, r.observationFreshness()
	}
	applyObservation(lease, &status, states, observation, r.now())
	status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
	return status, r.observationFreshness() / 2
}

func (r *PortForwardLeaseReconciler) drainStatus(ctx context.Context, lease *wayv1.PortForwardLease, evaluation leaseEvaluation) (wayv1.PortForwardLeaseStatus, time.Duration) {
	states := pendingLeaseStates(lease.Spec.ApplicationAdapterRef == nil)
	states[wayv1.ConditionAccepted] = evaluation.states[wayv1.ConditionAccepted]
	states[wayv1.ConditionResolvedRefs] = evaluation.states[wayv1.ConditionResolvedRefs]
	status := wayv1.PortForwardLeaseStatus{ObservedGeneration: lease.Generation, HandoffGeneration: lease.Status.HandoffGeneration, ActiveEndpoint: copyEndpoint(lease.Status.ActiveEndpoint)}
	status.ActiveEndpoint.Phase = wayv1.EndpointPhaseDraining
	withdrawal := WithdrawalFor(lease, evaluation.gateway)
	allocator := r.Allocator
	if allocator.Client == nil {
		allocator = ProviderPortAllocator{Client: r.Client, Now: r.Now}
	}
	providerPort, allocationErr := allocator.Reserve(ctx, lease, evaluation.gateway)
	if allocationErr != nil {
		unknownRuntimeStates(states)
		status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
		return status, r.observationFreshness() / 2
	}
	withdrawal.ProviderInternalPort = providerPort
	observation, err := r.Runtime.Withdraw(ctx, evaluation.gateway, withdrawal)
	if err != nil || !r.currentWithdrawal(observation, withdrawal) {
		unknownRuntimeStates(states)
		status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
		return status, r.observationFreshness() / 2
	}
	status.ActiveEndpoint = nil
	if evaluation.hasSelected {
		status.HandoffGeneration++
		status.ActiveEndpoint = endpointFor(evaluation.selected, wayv1.EndpointPhaseSelecting)
	}
	status.Conditions = wayv1.LeaseConditions(wayconditions.Build(lease.Status.Conditions, lease.Generation, r.now(), leaseConditionOrder, states))
	return status, time.Millisecond
}

func applyObservation(lease *wayv1.PortForwardLease, status *wayv1.PortForwardLeaseStatus, states map[string]wayconditions.State, observation Observation, now time.Time) {
	providerReady := observation.Provider != nil && observation.Provider.PublicAddress.IsValid() && observation.Provider.PublicAddress.IsGlobalUnicast() &&
		observation.Provider.PublicPort != 0 && observation.Provider.ExpiresAt.After(now)
	if providerReady {
		status.Provider = &wayv1.ProviderMappingStatus{PublicAddress: observation.Provider.PublicAddress.String(), PublicPort: int32(observation.Provider.PublicPort), ExpiresAt: metav1.NewTime(observation.Provider.ExpiresAt.UTC())}
	}
	if observation.GatewayRulesReady && providerReady {
		states[wayv1.ConditionGatewayRulesReady] = wayconditions.True(wayv1.ReasonGatewayRulesReady, "Exact gateway ingress and return-path rules are observed")
		states[wayv1.ConditionProgrammed] = wayconditions.True(wayv1.ReasonProgrammed, "Provider mapping and exact gateway rules are programmed")
		status.ActiveEndpoint.Phase = wayv1.EndpointPhaseActive
	}
	if observation.Delivered && observation.GatewayRulesReady && providerReady {
		states[wayv1.ConditionDelivered] = wayconditions.True(wayv1.ReasonDelivered, "Current lease generation is delivered")
	}
	acknowledged := lease.Spec.ApplicationAdapterRef == nil || observation.Acknowledged
	if acknowledged && observation.Delivered && observation.GatewayRulesReady && providerReady {
		states[wayv1.ConditionAcknowledged] = wayconditions.True(wayv1.ReasonAcknowledged, "Current application generation is acknowledged")
	}
	if observation.GatewayRulesReady && observation.Delivered && acknowledged && providerReady {
		states[wayv1.ConditionReady] = wayconditions.True(wayv1.ReasonReady, "SingleActive inbound path is ready")
	}
}

func (r *PortForwardLeaseReconciler) cleanup(ctx context.Context, lease *wayv1.PortForwardLease) (ctrl.Result, error) {
	if !containsString(lease.Finalizers, ProviderCleanupFinalizer) {
		return ctrl.Result{}, nil
	}
	gateway := &wayv1.VPNGateway{}
	err := r.reader().Get(ctx, client.ObjectKey{Namespace: string(lease.Spec.GatewayRef.Namespace), Name: string(lease.Spec.GatewayRef.Name)}, gateway)
	allocator := r.Allocator
	if allocator.Client == nil {
		allocator = ProviderPortAllocator{Client: r.Client, Now: r.Now}
	}
	reservation, reservationErr := allocator.Recover(ctx, lease, string(lease.Spec.GatewayRef.Namespace))
	if err != nil && reservationErr == nil {
		gateway = &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{
			Namespace: reservation.GatewayNamespace, Name: string(lease.Spec.GatewayRef.Name), UID: reservation.GatewayUID,
		}}
		err = nil
	}
	withdrawn := false
	if errors.Is(reservationErr, ErrProviderReservationNotFound) && lease.Status.ActiveEndpoint == nil && lease.Status.Provider == nil {
		// The finalizer is installed before the first runtime action. An absent
		// reservation in that exact pre-action state means there is nothing to
		// release; cleanup must not acquire a new provider mapping.
		withdrawn = true
	}
	if err == nil && r.Runtime != nil && gateway.UID != "" {
		if reservationErr == nil {
			intent := WithdrawalFor(lease, gateway)
			intent.ReleaseProvider = true
			intent.ProviderInternalPort = reservation.Port
			observation, withdrawErr := r.Runtime.Withdraw(ctx, gateway, intent)
			withdrawn = withdrawErr == nil && r.currentWithdrawal(observation, intent)
		}
	}
	deadline := lease.DeletionTimestamp.Add(r.cleanupTimeout())
	if !withdrawn && r.now().Before(deadline) {
		return ctrl.Result{RequeueAfter: minDuration(deadline.Sub(r.now()), r.observationFreshness()/2)}, nil
	}
	quarantineUntil := r.now().Add(r.cleanupTimeout())
	if !withdrawn && err == nil && r.Runtime != nil {
		intent := WithdrawalFor(lease, gateway)
		intent.ReleaseProvider = true
		if quarantineErr := r.Runtime.Quarantine(ctx, gateway, intent, quarantineUntil); quarantineErr != nil {
			return ctrl.Result{}, quarantineErr
		}
	}
	if err == nil {
		if quarantineErr := allocator.Quarantine(ctx, lease, gateway, quarantineUntil); quarantineErr != nil {
			return ctrl.Result{}, quarantineErr
		}
	}
	before := lease.DeepCopy()
	lease.Finalizers = removeString(lease.Finalizers, ProviderCleanupFinalizer)
	if patchErr := r.Patch(ctx, lease, client.MergeFrom(before), client.FieldOwner(wayv1.FieldManagerLeaseController)); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("remove provider cleanup finalizer: %w", patchErr)
	}
	return ctrl.Result{}, nil
}

func (r *PortForwardLeaseReconciler) applyStatus(ctx context.Context, lease *wayv1.PortForwardLease, status wayv1.PortForwardLeaseStatus) error {
	updated := lease.DeepCopy()
	updated.Status = status
	// Status advances the handoff generation around external side effects. Use
	// an optimistic-concurrency update so a reconcile that started before a
	// controller handover cannot overwrite a newer, already-delivered
	// generation with stale state.
	if err := r.Status().Update(ctx, updated); err != nil {
		return fmt.Errorf("apply PortForwardLease status: %w", err)
	}
	return nil
}

func (r *PortForwardLeaseReconciler) currentObservation(observation Observation, intent Intent) bool {
	return ExactObservation(observation, intent) && !observation.ObservedAt.Before(r.now().Add(-r.observationFreshness())) && !observation.ObservedAt.After(r.now().Add(time.Minute))
}

func (r *PortForwardLeaseReconciler) currentWithdrawal(observation Observation, intent WithdrawalIntent) bool {
	return ExactWithdrawal(observation, intent) && !observation.ObservedAt.Before(r.now().Add(-r.observationFreshness())) && !observation.ObservedAt.After(r.now().Add(time.Minute))
}

func endpointFor(candidate Candidate, phase wayv1.EndpointHandoffPhase) *wayv1.ActiveLeaseEndpoint {
	return &wayv1.ActiveLeaseEndpoint{ServiceUID: candidate.ServiceUID, EndpointSliceUID: candidate.EndpointSliceUID, PodUID: candidate.PodUID, Phase: phase}
}

func copyEndpoint(value *wayv1.ActiveLeaseEndpoint) *wayv1.ActiveLeaseEndpoint {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func pendingLeaseStates(noAdapter bool) map[string]wayconditions.State {
	acknowledgement := wayconditions.False(wayv1.ReasonAcknowledgementPending, "Application acknowledgement is pending")
	if noAdapter {
		acknowledgement = wayconditions.False(wayv1.ReasonAcknowledgementPending, "Current neutral delivery is pending")
	}
	return map[string]wayconditions.State{
		wayv1.ConditionAccepted:          wayconditions.False(wayv1.ReasonInvalid, "Lease intent is not accepted"),
		wayv1.ConditionResolvedRefs:      wayconditions.False(wayv1.ReasonRefNotFound, "Lease references are unresolved"),
		wayv1.ConditionProgrammed:        wayconditions.False(wayv1.ReasonPending, "Provider mapping and gateway rules are pending"),
		wayv1.ConditionReady:             wayconditions.False(wayv1.ReasonNotReady, "SingleActive inbound path is not ready"),
		wayv1.ConditionGatewayRulesReady: wayconditions.False(wayv1.ReasonGatewayRulesPending, "Exact gateway rules are pending"),
		wayv1.ConditionDelivered:         wayconditions.False(wayv1.ReasonDeliveryPending, "Current lease generation delivery is pending"),
		wayv1.ConditionAcknowledged:      acknowledgement,
	}
}

func unknownLeaseStates(states map[string]wayconditions.State, message string) {
	for _, conditionType := range leaseConditionOrder {
		states[conditionType] = wayconditions.Unknown(message)
	}
}

func unknownRuntimeStates(states map[string]wayconditions.State) {
	for _, conditionType := range []string{wayv1.ConditionProgrammed, wayv1.ConditionReady, wayv1.ConditionGatewayRulesReady, wayv1.ConditionDelivered, wayv1.ConditionAcknowledged} {
		states[conditionType] = wayconditions.Unknown("Gateway runtime observation is unavailable")
	}
}

func gatewayAccepted(gateway *wayv1.VPNGateway) bool {
	return wayconditions.CurrentTrue(gateway.Status.Conditions, wayv1.ConditionAccepted, gateway.Status.ObservedGeneration, gateway.Generation)
}

func gatewayReady(gateway *wayv1.VPNGateway) bool {
	return wayconditions.CurrentTrue(gateway.Status.Conditions, wayv1.ConditionReady, gateway.Status.ObservedGeneration, gateway.Generation)
}

func hasGatewayFeature(gateway *wayv1.VPNGateway, feature wayv1.FeatureName) bool {
	for _, supported := range gateway.Status.SupportedFeatures {
		if supported == feature {
			return true
		}
	}
	return false
}

func LeaseBackendIndexValues(object client.Object) []string {
	lease := object.(*wayv1.PortForwardLease)
	return []string{string(lease.Spec.BackendRef.Name)}
}

func (r *PortForwardLeaseReconciler) SetupWithManager(manager ctrl.Manager) error {
	if r.Client == nil {
		r.Client = manager.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = manager.GetAPIReader()
	}
	if err := manager.GetFieldIndexer().IndexField(context.Background(), &wayv1.PortForwardLease{}, reference.LeaseGatewayIndex, reference.LeaseGatewayIndexValues); err != nil {
		return fmt.Errorf("index lease gateway references: %w", err)
	}
	if err := manager.GetFieldIndexer().IndexField(context.Background(), &wayv1.PortForwardLease{}, LeaseBackendIndex, LeaseBackendIndexValues); err != nil {
		return fmt.Errorf("index lease Service backends: %w", err)
	}
	return ctrl.NewControllerManagedBy(manager).For(&wayv1.PortForwardLease{}).
		Watches(&wayv1.VPNGateway{}, handler.EnqueueRequestsFromMapFunc(r.leasesForGateway)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.leasesForNamespace)).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.leasesForService)).
		Watches(&discoveryv1.EndpointSlice{}, handler.EnqueueRequestsFromMapFunc(r.leasesForEndpointSlice)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.leasesInObjectNamespace)).
		Watches(&wayv1.VPNWorkloadBinding{}, handler.EnqueueRequestsFromMapFunc(r.leasesInObjectNamespace)).
		Watches(&wayv1.WorkloadAdapter{}, handler.EnqueueRequestsFromMapFunc(r.leasesInObjectNamespace)).
		Complete(r)
}

func (r *PortForwardLeaseReconciler) leasesForGateway(ctx context.Context, object client.Object) []reconcile.Request {
	return (reference.DependencyMapper{Client: r.Client}).LeasesForGateway(ctx, object)
}

func (r *PortForwardLeaseReconciler) leasesForNamespace(ctx context.Context, object client.Object) []reconcile.Request {
	return (reference.DependencyMapper{Client: r.Client}).LeasesForNamespace(ctx, object)
}

func (r *PortForwardLeaseReconciler) leasesForService(ctx context.Context, object client.Object) []reconcile.Request {
	leases := &wayv1.PortForwardLeaseList{}
	if err := r.List(ctx, leases, client.InNamespace(object.GetNamespace()), client.MatchingFields{LeaseBackendIndex: object.GetName()}); err != nil {
		return nil
	}
	return requestsForLeases(leases.Items)
}

func (r *PortForwardLeaseReconciler) leasesForEndpointSlice(ctx context.Context, object client.Object) []reconcile.Request {
	name := object.GetLabels()[discoveryv1.LabelServiceName]
	if name == "" {
		return nil
	}
	return r.leasesForService(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: object.GetNamespace(), Name: name}})
}

func (r *PortForwardLeaseReconciler) leasesInObjectNamespace(ctx context.Context, object client.Object) []reconcile.Request {
	leases := &wayv1.PortForwardLeaseList{}
	if err := r.List(ctx, leases, client.InNamespace(object.GetNamespace())); err != nil {
		return nil
	}
	return requestsForLeases(leases.Items)
}

func requestsForLeases(leases []wayv1.PortForwardLease) []reconcile.Request {
	requests := make([]reconcile.Request, 0, len(leases))
	for index := range leases {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&leases[index])})
	}
	return reference.SortedUniqueRequests(requests)
}

func (r *PortForwardLeaseReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *PortForwardLeaseReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *PortForwardLeaseReconciler) observationFreshness() time.Duration {
	if r.ObservationFreshness > 0 {
		return r.ObservationFreshness
	}
	return DefaultObservationFreshness
}

func (r *PortForwardLeaseReconciler) cleanupTimeout() time.Duration {
	if r.CleanupTimeout > 0 {
		return r.CleanupTimeout
	}
	return DefaultCleanupTimeout
}

func errorsAsResolution(err error, target **ResolutionError) bool {
	return errors.As(err, target)
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func removeString(values []string, value string) []string {
	result := make([]string, 0, len(values))
	for _, candidate := range values {
		if candidate != value {
			result = append(result, candidate)
		}
	}
	return result
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

var _ reconcile.Reconciler = (*PortForwardLeaseReconciler)(nil)
