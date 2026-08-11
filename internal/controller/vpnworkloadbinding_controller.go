// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strconv"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	wayconditions "github.com/Amoenus/waycloak/internal/conditions"
	"github.com/Amoenus/waycloak/internal/enrollment"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	DataplaneCleanupFinalizer = "networking.waycloak.io/dataplane-cleanup"
	podRouteIndex             = "networking.waycloak.io/pod-route"
	routeGatewayIndex         = "networking.waycloak.io/route-gateway"
	bindingGatewayIndex       = "networking.waycloak.io/binding-gateway"
	defaultCleanupTimeout     = 10 * time.Minute
	defaultObservationTTL     = 30 * time.Second
)

// PodBindingReconciler creates immutable desired state and an atomic address
// reservation. It deliberately does not infer readiness from object creation.
type PodBindingReconciler struct {
	client.Client
	APIReader client.Reader
	Now       func() time.Time
}

func (r *PodBindingReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, request.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	routeName := pod.Labels[enrollment.RouteLabel]
	if routeName == "" || pod.UID == "" || pod.Spec.NodeName == "" || !pod.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	route := &wayv1.VPNEgressRoute{}
	if err := r.reader().Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: routeName}, route); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("resolve enrolled route: %w", err)
	}
	if !routeEligible(route) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	parent := route.Spec.ParentRefs[0]
	gateway := &wayv1.VPNGateway{}
	if err := r.reader().Get(ctx, client.ObjectKey{Namespace: string(parent.Namespace), Name: string(parent.Name)}, gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("resolve binding gateway: %w", err)
	}
	if gateway.UID == "" || !gatewayEligible(gateway) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	pool, err := waybinding.ParseOverlayCIDR(gateway)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	network, err := workloadNetworkIntent(gateway)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	name := waybinding.BindingName(pod.UID)
	existing := &wayv1.VPNWorkloadBinding{}
	err = r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: name}, existing)
	if err == nil {
		if existing.Spec.PodRef.UID != wayv1.ObjectUID(pod.UID) {
			return ctrl.Result{}, errors.New("UID-derived binding name collision")
		}
		if !bindingMatches(existing, pod, route, gateway) {
			if existing.DeletionTimestamp.IsZero() {
				if err := r.Delete(ctx, existing); err != nil {
					return ctrl.Result{}, fmt.Errorf("withdraw obsolete workload binding: %w", err)
				}
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if !reflect.DeepEqual(existing.Spec.Network, network) {
			updated := existing.DeepCopy()
			updated.Spec.Network = network
			if err := r.Patch(ctx, updated, client.MergeFrom(existing), client.FieldOwner(wayv1.FieldManagerBindingController)); err != nil {
				return ctrl.Result{}, fmt.Errorf("update credential-free binding network intent: %w", err)
			}
			return ctrl.Result{}, nil
		}
		if _, err := (waybinding.Allocator{Client: r.Client, Reader: r.reader(), Now: r.Now}).Ensure(ctx, gateway, existing); err != nil {
			if errors.Is(err, waybinding.ErrReservationConflict) && existing.DeletionTimestamp.IsZero() {
				_ = r.Delete(ctx, existing)
			}
			return ctrl.Result{}, fmt.Errorf("validate binding reservation: %w", err)
		}
		return ctrl.Result{}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("read UID-bound workload binding: %w", err)
	}

	reservation, err := (waybinding.Allocator{Client: r.Client, Reader: r.reader(), Now: r.Now}).Reserve(ctx, gateway, pod.UID, pool)
	if err != nil {
		if errors.Is(err, waybinding.ErrPoolExhausted) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	controllerOwner := true
	binding := &wayv1.VPNWorkloadBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: pod.Namespace, Finalizers: []string{DataplaneCleanupFinalizer},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID, Controller: &controllerOwner,
			}},
		},
		Spec: wayv1.VPNWorkloadBindingSpec{
			PodRef:     wayv1.LocalUIDReference{Name: wayv1.ObjectName(pod.Name), UID: wayv1.ObjectUID(pod.UID)},
			RouteRef:   wayv1.LocalUIDReference{Name: wayv1.ObjectName(route.Name), UID: wayv1.ObjectUID(route.UID)},
			GatewayRef: wayv1.NamespacedUIDReference{Namespace: wayv1.NamespaceName(gateway.Namespace), Name: wayv1.ObjectName(gateway.Name), UID: wayv1.ObjectUID(gateway.UID)},
			NodeName:   wayv1.ObjectName(pod.Spec.NodeName),
			Allocation: wayv1.WorkloadAllocation{Identity: reservation.Identity, Address: reservation.Address.String()},
			Network:    network,
		},
	}
	if err := r.Create(ctx, binding, client.FieldOwner(wayv1.FieldManagerBindingController)); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("persist UID-bound workload binding: %w", err)
	}
	return ctrl.Result{}, nil
}

func workloadNetworkIntent(gateway *wayv1.VPNGateway) (wayv1.WorkloadNetworkIntent, error) {
	values := make(map[wayv1.QualifiedName]string, len(gateway.Status.Addresses))
	for _, address := range gateway.Status.Addresses {
		values[address.Type] = address.Value
	}
	required := []wayv1.QualifiedName{
		wayv1.GatewayAddressTypeOverlayCIDR, wayv1.GatewayAddressTypeOverlayAddress,
		wayv1.GatewayAddressTypeUnderlayEndpoint, wayv1.GatewayAddressTypeOverlayHealthPort,
		wayv1.GatewayAddressTypeVNI, wayv1.GatewayAddressTypeMTU,
	}
	for _, addressType := range required {
		if values[addressType] == "" {
			return wayv1.WorkloadNetworkIntent{}, fmt.Errorf("gateway status address %q is unavailable", addressType)
		}
	}
	overlay, err := netip.ParsePrefix(values[wayv1.GatewayAddressTypeOverlayCIDR])
	if err != nil {
		return wayv1.WorkloadNetworkIntent{}, fmt.Errorf("parse gateway overlay CIDR: %w", err)
	}
	gatewayAddress, err := netip.ParseAddr(values[wayv1.GatewayAddressTypeOverlayAddress])
	if err != nil || !overlay.Contains(gatewayAddress) {
		return wayv1.WorkloadNetworkIntent{}, errors.New("gateway overlay address is invalid")
	}
	endpoint, err := netip.ParseAddrPort(values[wayv1.GatewayAddressTypeUnderlayEndpoint])
	if err != nil || endpoint.Port() == 0 {
		return wayv1.WorkloadNetworkIntent{}, errors.New("gateway underlay endpoint is invalid")
	}
	healthPort, err := strconv.ParseInt(values[wayv1.GatewayAddressTypeOverlayHealthPort], 10, 32)
	if err != nil || healthPort < 1 || healthPort > 65535 {
		return wayv1.WorkloadNetworkIntent{}, errors.New("gateway overlay health port is invalid")
	}
	vni, err := strconv.ParseInt(values[wayv1.GatewayAddressTypeVNI], 10, 32)
	if err != nil || vni < 1 || vni > 16777215 {
		return wayv1.WorkloadNetworkIntent{}, errors.New("gateway VNI is invalid")
	}
	mtu, err := strconv.ParseInt(values[wayv1.GatewayAddressTypeMTU], 10, 32)
	if err != nil || mtu < 576 || mtu > 9000 {
		return wayv1.WorkloadNetworkIntent{}, errors.New("gateway MTU is invalid")
	}
	return wayv1.WorkloadNetworkIntent{
		GatewayGeneration: gateway.Generation,
		OverlayCIDR:       overlay.Masked().String(), GatewayAddress: gatewayAddress.String(),
		GatewayEndpoint: endpoint.String(), GatewayHealthPort: int32(healthPort),
		VNI: int32(vni), MTU: int32(mtu), ClusterTraffic: gateway.Spec.ClusterTraffic,
	}, nil
}

func (r *PodBindingReconciler) SetupWithManager(manager ctrl.Manager) error {
	if r.Client == nil {
		r.Client = manager.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = manager.GetAPIReader()
	}
	if err := manager.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, podRouteIndex, func(object client.Object) []string {
		if value := object.GetLabels()[enrollment.RouteLabel]; value != "" {
			return []string{value}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("index enrolled Pod routes: %w", err)
	}
	if err := manager.GetFieldIndexer().IndexField(context.Background(), &wayv1.VPNEgressRoute{}, routeGatewayIndex, func(object client.Object) []string {
		route, ok := object.(*wayv1.VPNEgressRoute)
		if !ok {
			return nil
		}
		keys := make([]string, 0, len(route.Spec.ParentRefs))
		for _, parent := range route.Spec.ParentRefs {
			if parent.Group == wayv1.GroupName && parent.Kind == "VPNGateway" && parent.Namespace != "" && parent.Name != "" {
				keys = append(keys, gatewayReferenceKey(string(parent.Namespace), string(parent.Name)))
			}
		}
		return keys
	}); err != nil {
		return fmt.Errorf("index egress route gateways: %w", err)
	}
	return ctrl.NewControllerManagedBy(manager).
		For(&corev1.Pod{}).
		Watches(&wayv1.VPNEgressRoute{}, handler.EnqueueRequestsFromMapFunc(r.podsForRoute)).
		Watches(&wayv1.VPNGateway{}, handler.EnqueueRequestsFromMapFunc(r.podsForGateway)).
		Complete(r)
}

func (r *PodBindingReconciler) podsForRoute(ctx context.Context, object client.Object) []reconcile.Request {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(object.GetNamespace()), client.MatchingFields{podRouteIndex: object.GetName()}); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(pods.Items))
	for i := range pods.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pods.Items[i])})
	}
	return requests
}

func (r *PodBindingReconciler) podsForGateway(ctx context.Context, object client.Object) []reconcile.Request {
	routes := &wayv1.VPNEgressRouteList{}
	if err := r.List(ctx, routes, client.MatchingFields{routeGatewayIndex: gatewayReferenceKey(object.GetNamespace(), object.GetName())}); err != nil {
		return nil
	}
	seen := make(map[client.ObjectKey]struct{})
	requests := make([]reconcile.Request, 0)
	for i := range routes.Items {
		for _, request := range r.podsForRoute(ctx, &routes.Items[i]) {
			if _, exists := seen[request.NamespacedName]; exists {
				continue
			}
			seen[request.NamespacedName] = struct{}{}
			requests = append(requests, request)
		}
	}
	return requests
}

func (r *PodBindingReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// VPNWorkloadBindingReconciler owns status and bounded external cleanup. Only
// a fresh, exact observation relayed by the authenticated node agent can make
// Ready true.
type VPNWorkloadBindingReconciler struct {
	client.Client
	APIReader      client.Reader
	Now            func() time.Time
	CleanupTimeout time.Duration
	ObservationTTL time.Duration
}

type bindingGatewayObservation struct {
	resolved wayconditions.State
	ready    wayconditions.State
}

func (r *VPNWorkloadBindingReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	binding := &wayv1.VPNWorkloadBinding{}
	if err := r.Get(ctx, request.NamespacedName, binding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !binding.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, binding)
	}
	desired, requeue := r.desiredStatus(binding, r.observeGateway(ctx, binding))
	if reflect.DeepEqual(binding.Status, desired) {
		return ctrl.Result{RequeueAfter: requeue}, nil
	}
	if err := r.applyStatus(ctx, binding, desired); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *VPNWorkloadBindingReconciler) desiredStatus(binding *wayv1.VPNWorkloadBinding, gatewayObservation bindingGatewayObservation) (wayv1.VPNWorkloadBindingStatus, time.Duration) {
	status := binding.Status
	now := r.now()
	states := map[string]wayconditions.State{
		wayv1.ConditionAccepted:     wayconditions.True(wayv1.ReasonAccepted, "Binding intent is accepted"),
		wayv1.ConditionResolvedRefs: gatewayObservation.resolved,
		wayv1.ConditionProgrammed:   wayconditions.False(wayv1.ReasonPending, "Binding programming is pending"),
		wayv1.ConditionReady:        wayconditions.Unknown("Live protected-path observation is unavailable"),
		wayv1.ConditionNodeReady:    wayconditions.Unknown("Node-agent observation is unavailable"),
	}
	var requeue time.Duration
	exact := status.ObservedPodUID == binding.Spec.PodRef.UID && status.ObservedGatewayUID == binding.Spec.GatewayRef.UID &&
		status.Agent != nil && status.Agent.NodeName == binding.Spec.NodeName
	if status.AppliedGeneration > 0 && status.AppliedGeneration != binding.Generation {
		states[wayv1.ConditionProgrammed] = wayconditions.False(wayv1.ReasonStaleGeneration, "Node applied generation is stale")
	} else if status.AppliedGeneration == binding.Generation && exact {
		states[wayv1.ConditionProgrammed] = wayconditions.True(wayv1.ReasonProgrammed, "Current binding generation is applied")
		age := now.Sub(status.Agent.ObservedAt.Time)
		if age < 0 {
			age = 0
		}
		if age < r.observationTTL() {
			states[wayv1.ConditionNodeReady] = wayconditions.True(wayv1.ReasonNodeReady, "Current node data plane is observed")
			states[wayv1.ConditionReady] = wayconditions.True(wayv1.ReasonReady, "Exact Pod protected path is live")
			requeue = r.observationTTL() - age
		} else {
			states[wayv1.ConditionNodeReady] = wayconditions.False(wayv1.ReasonNodeNotReady, "Node-agent observation is stale")
			states[wayv1.ConditionReady] = wayconditions.False(wayv1.ReasonNotReady, "Protected-path observation is stale")
		}
	} else if status.AppliedGeneration == binding.Generation && !exact {
		states[wayv1.ConditionProgrammed] = wayconditions.False(wayv1.ReasonApplyFailed, "Applied binding identity does not match desired state")
		states[wayv1.ConditionNodeReady] = wayconditions.False(wayv1.ReasonNodeNotReady, "Node-agent identity does not match binding")
		states[wayv1.ConditionReady] = wayconditions.False(wayv1.ReasonNotReady, "Protected-path identity does not match binding")
	}
	if gatewayObservation.ready.Status != metav1.ConditionTrue {
		states[wayv1.ConditionReady] = gatewayObservation.ready
	}
	status.ObservedGeneration = binding.Generation
	status.Conditions = wayv1.BindingConditions(wayconditions.Build(binding.Status.Conditions, binding.Generation, now,
		[]string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady, wayv1.ConditionNodeReady}, states))
	return status, requeue
}

func (r *VPNWorkloadBindingReconciler) observeGateway(ctx context.Context, binding *wayv1.VPNWorkloadBinding) bindingGatewayObservation {
	observation := bindingGatewayObservation{
		resolved: wayconditions.Unknown("Exact gateway reference observation is unavailable"),
		ready:    wayconditions.Unknown("Referenced gateway readiness observation is unavailable"),
	}
	gateway := &wayv1.VPNGateway{}
	err := r.reader().Get(ctx, client.ObjectKey{Namespace: string(binding.Spec.GatewayRef.Namespace), Name: string(binding.Spec.GatewayRef.Name)}, gateway)
	if apierrors.IsNotFound(err) {
		observation.resolved = wayconditions.False(wayv1.ReasonRefNotFound, "Exact gateway reference is unavailable")
		observation.ready = wayconditions.False(wayv1.ReasonNotReady, "Referenced gateway data plane is unavailable")
		return observation
	}
	if err != nil {
		return observation
	}
	if gateway.UID == "" || wayv1.ObjectUID(gateway.UID) != binding.Spec.GatewayRef.UID || !gateway.DeletionTimestamp.IsZero() {
		observation.resolved = wayconditions.False(wayv1.ReasonIncompatibleRef, "Exact gateway reference no longer matches")
		observation.ready = wayconditions.False(wayv1.ReasonNotReady, "Referenced gateway data plane is unavailable")
		return observation
	}
	observation.resolved = wayconditions.True(wayv1.ReasonResolvedRefs, "UID-bound references are resolved")
	if binding.Spec.Network.GatewayGeneration != gateway.Generation {
		observation.ready = wayconditions.False(wayv1.ReasonNotReady, "Binding network intent does not match the current gateway generation")
		return observation
	}
	if !gatewayEligible(gateway) {
		observation.ready = wayconditions.False(wayv1.ReasonNotReady, "Referenced gateway data plane is not ready")
		return observation
	}
	observation.ready = wayconditions.True(wayv1.ReasonReady, "Referenced gateway live data plane is ready")
	return observation
}

func (r *VPNWorkloadBindingReconciler) reconcileDeletion(ctx context.Context, binding *wayv1.VPNWorkloadBinding) (ctrl.Result, error) {
	if !containsString(binding.Finalizers, DataplaneCleanupFinalizer) {
		return ctrl.Result{}, nil
	}
	desired := r.deletingStatus(binding)
	if !reflect.DeepEqual(binding.Status, desired) {
		if err := r.applyStatus(ctx, binding, desired); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	allocator := waybinding.Allocator{Client: r.Client, Reader: r.reader(), Now: r.Now}
	if r.withdrawalConfirmed(binding) {
		if err := allocator.Release(ctx, binding); err != nil {
			return ctrl.Result{}, fmt.Errorf("release withdrawn address reservation: %w", err)
		}
		return ctrl.Result{}, r.removeFinalizer(ctx, binding)
	}
	deadline := binding.DeletionTimestamp.Add(r.cleanupTimeout())
	if r.now().Before(deadline) {
		return ctrl.Result{RequeueAfter: deadline.Sub(r.now())}, nil
	}
	if err := allocator.Quarantine(ctx, binding); err != nil {
		return ctrl.Result{}, fmt.Errorf("quarantine unconfirmed address withdrawal: %w", err)
	}
	return ctrl.Result{}, r.removeFinalizer(ctx, binding)
}

func (r *VPNWorkloadBindingReconciler) withdrawalConfirmed(binding *wayv1.VPNWorkloadBinding) bool {
	if binding.Status.AppliedGeneration != 0 || binding.Status.ObservedPodUID != binding.Spec.PodRef.UID ||
		binding.Status.ObservedGatewayUID != binding.Spec.GatewayRef.UID || binding.Status.Agent == nil ||
		binding.Status.Agent.NodeName != binding.Spec.NodeName {
		return false
	}
	age := r.now().Sub(binding.Status.Agent.ObservedAt.Time)
	return age >= 0 && age < r.observationTTL()
}

func (r *VPNWorkloadBindingReconciler) deletingStatus(binding *wayv1.VPNWorkloadBinding) wayv1.VPNWorkloadBindingStatus {
	status := binding.Status
	status.ObservedGeneration = binding.Generation
	status.Conditions = wayv1.BindingConditions(wayconditions.Build(binding.Status.Conditions, binding.Generation, r.now(),
		[]string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady, wayv1.ConditionNodeReady}, map[string]wayconditions.State{
			wayv1.ConditionAccepted:     wayconditions.False(wayv1.ReasonDeleting, "Binding is deleting"),
			wayv1.ConditionResolvedRefs: wayconditions.True(wayv1.ReasonResolvedRefs, "UID-bound references remain resolved for cleanup"),
			wayv1.ConditionProgrammed:   wayconditions.False(wayv1.ReasonPending, "Binding programming is withdrawn"),
			wayv1.ConditionReady:        wayconditions.False(wayv1.ReasonDeleting, "Protected path is withdrawing"),
			wayv1.ConditionNodeReady:    wayconditions.False(wayv1.ReasonNodeNotReady, "Node cleanup is pending"),
		}))
	return status
}

func (r *VPNWorkloadBindingReconciler) applyStatus(ctx context.Context, binding *wayv1.VPNWorkloadBinding, status wayv1.VPNWorkloadBindingStatus) error {
	apply := &wayv1.VPNWorkloadBinding{TypeMeta: metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNWorkloadBinding"}, ObjectMeta: metav1.ObjectMeta{Name: binding.Name, Namespace: binding.Namespace}, Status: status}
	data, err := json.Marshal(apply)
	if err != nil {
		return err
	}
	if err := r.SubResource("status").Patch(ctx, apply, client.RawPatch(types.ApplyPatchType, data), client.FieldOwner(wayv1.FieldManagerBindingController)); err != nil {
		return fmt.Errorf("apply VPNWorkloadBinding status: %w", err)
	}
	return nil
}

func (r *VPNWorkloadBindingReconciler) removeFinalizer(ctx context.Context, binding *wayv1.VPNWorkloadBinding) error {
	copy := binding.DeepCopy()
	copy.Finalizers = removeString(copy.Finalizers, DataplaneCleanupFinalizer)
	if err := r.Patch(ctx, copy, client.MergeFrom(binding), client.FieldOwner(wayv1.FieldManagerBindingController)); err != nil {
		return fmt.Errorf("release bounded data-plane cleanup finalizer: %w", err)
	}
	return nil
}

func (r *VPNWorkloadBindingReconciler) SetupWithManager(manager ctrl.Manager) error {
	if r.Client == nil {
		r.Client = manager.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = manager.GetAPIReader()
	}
	if err := manager.GetFieldIndexer().IndexField(context.Background(), &wayv1.VPNWorkloadBinding{}, bindingGatewayIndex, func(object client.Object) []string {
		binding, ok := object.(*wayv1.VPNWorkloadBinding)
		if !ok || binding.Spec.GatewayRef.Namespace == "" || binding.Spec.GatewayRef.Name == "" {
			return nil
		}
		return []string{gatewayReferenceKey(string(binding.Spec.GatewayRef.Namespace), string(binding.Spec.GatewayRef.Name))}
	}); err != nil {
		return fmt.Errorf("index workload binding gateways: %w", err)
	}
	return ctrl.NewControllerManagedBy(manager).
		For(&wayv1.VPNWorkloadBinding{}).
		Watches(&wayv1.VPNGateway{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForGateway)).
		Complete(r)
}

func (r *VPNWorkloadBindingReconciler) bindingsForGateway(ctx context.Context, object client.Object) []reconcile.Request {
	bindings := &wayv1.VPNWorkloadBindingList{}
	if err := r.List(ctx, bindings, client.MatchingFields{bindingGatewayIndex: gatewayReferenceKey(object.GetNamespace(), object.GetName())}); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(bindings.Items))
	for i := range bindings.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bindings.Items[i])})
	}
	return requests
}

func gatewayReferenceKey(namespace, name string) string { return namespace + "/" + name }

func routeEligible(route *wayv1.VPNEgressRoute) bool {
	if route == nil || route.UID == "" || !route.DeletionTimestamp.IsZero() || len(route.Spec.ParentRefs) != 1 || len(route.Status.Parents) != 1 {
		return false
	}
	parent := route.Status.Parents[0]
	return parent.ControllerName == RouteControllerName && reflect.DeepEqual(parent.ParentRef, route.Spec.ParentRefs[0]) &&
		wayconditions.CurrentTrue(route.Status.Conditions, wayv1.ConditionReady, route.Status.ObservedGeneration, route.Generation) &&
		wayconditions.CurrentTrue(parent.Conditions, wayv1.ConditionReady, route.Status.ObservedGeneration, route.Generation)
}

func gatewayEligible(gateway *wayv1.VPNGateway) bool {
	return gateway != nil && gateway.UID != "" && gateway.DeletionTimestamp.IsZero() &&
		wayconditions.CurrentTrue(gateway.Status.Conditions, wayv1.ConditionReady, gateway.Status.ObservedGeneration, gateway.Generation)
}

func bindingMatches(binding *wayv1.VPNWorkloadBinding, pod *corev1.Pod, route *wayv1.VPNEgressRoute, gateway *wayv1.VPNGateway) bool {
	return binding.Spec.PodRef.Name == wayv1.ObjectName(pod.Name) && binding.Spec.PodRef.UID == wayv1.ObjectUID(pod.UID) &&
		binding.Spec.RouteRef.Name == wayv1.ObjectName(route.Name) && binding.Spec.RouteRef.UID == wayv1.ObjectUID(route.UID) &&
		binding.Spec.GatewayRef.Namespace == wayv1.NamespaceName(gateway.Namespace) && binding.Spec.GatewayRef.Name == wayv1.ObjectName(gateway.Name) && binding.Spec.GatewayRef.UID == wayv1.ObjectUID(gateway.UID) &&
		binding.Spec.NodeName == wayv1.ObjectName(pod.Spec.NodeName) && binding.DeletionTimestamp.IsZero()
}

func (r *VPNWorkloadBindingReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
func (r *VPNWorkloadBindingReconciler) cleanupTimeout() time.Duration {
	if r.CleanupTimeout > 0 {
		return r.CleanupTimeout
	}
	return defaultCleanupTimeout
}
func (r *VPNWorkloadBindingReconciler) observationTTL() time.Duration {
	if r.ObservationTTL > 0 {
		return r.ObservationTTL
	}
	return defaultObservationTTL
}
func (r *VPNWorkloadBindingReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func removeString(values []string, unwanted string) []string {
	result := values[:0]
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}

var _ reconcile.Reconciler = (*PodBindingReconciler)(nil)
var _ reconcile.Reconciler = (*VPNWorkloadBindingReconciler)(nil)
