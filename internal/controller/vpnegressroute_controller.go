// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	RouteControllerName wayv1.ControllerName = "networking.waycloak.io/route-controller"
	routeParentIndex    string               = "networking.waycloak.io/route-parent"
	routeFieldManager   string               = "waycloak-route-core"
)

type RouteReferenceAuthorizer interface {
	Authorize(context.Context, *wayv1.VPNEgressRoute, wayv1.GatewayParentReference) (bool, error)
}

type SameNamespaceRouteAuthorizer struct{}

func (SameNamespaceRouteAuthorizer) Authorize(_ context.Context, route *wayv1.VPNEgressRoute, parent wayv1.GatewayParentReference) (bool, error) {
	return route.Namespace == string(parent.Namespace), nil
}

type VPNEgressRouteReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Authorizer RouteReferenceAuthorizer
	Now        func() time.Time
}

func (r *VPNEgressRouteReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	route := &wayv1.VPNEgressRoute{}
	if err := r.Get(ctx, request.NamespacedName, route); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !route.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	desired := r.desiredStatus(ctx, route)
	if reflect.DeepEqual(route.Status, desired) {
		return ctrl.Result{}, nil
	}
	apply := &wayv1.VPNEgressRoute{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNEgressRoute"},
		ObjectMeta: metav1.ObjectMeta{Name: route.Name, Namespace: route.Namespace},
		Status:     desired,
	}
	data, err := json.Marshal(apply)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("encode VPNEgressRoute status apply: %w", err)
	}
	if err := r.SubResource("status").Patch(ctx, apply, client.RawPatch(types.ApplyPatchType, data), client.FieldOwner(routeFieldManager)); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply VPNEgressRoute status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *VPNEgressRouteReconciler) desiredStatus(ctx context.Context, route *wayv1.VPNEgressRoute) wayv1.VPNEgressRouteStatus {
	parent := route.Spec.ParentRefs[0]
	statuses := routeConditionSet{
		accepted:   conditionState{status: metav1.ConditionTrue, reason: "Accepted", message: "Route intent is accepted"},
		resolved:   conditionState{status: metav1.ConditionFalse, reason: "RefNotFound", message: "Parent reference is unresolved"},
		programmed: conditionState{status: metav1.ConditionFalse, reason: "Pending", message: "Route programming is pending"},
		ready:      conditionState{status: metav1.ConditionFalse, reason: "NotReady", message: "Protected route is not ready"},
	}
	authorizer := r.Authorizer
	if authorizer == nil {
		authorizer = SameNamespaceRouteAuthorizer{}
	}
	authorized, err := authorizer.Authorize(ctx, route, parent)
	switch {
	case err != nil:
		statuses.resolved = unavailable("Reference authorization is unavailable")
		statuses.programmed = unavailable("Route programming observation is unavailable")
		statuses.ready = unavailable("Protected route observation is unavailable")
	case !authorized:
		statuses.resolved = conditionState{status: metav1.ConditionFalse, reason: "RefNotPermitted", message: "Parent reference is not permitted"}
	default:
		gateway := &wayv1.VPNGateway{}
		err = r.Get(ctx, types.NamespacedName{Namespace: string(parent.Namespace), Name: string(parent.Name)}, gateway)
		switch {
		case apierrors.IsNotFound(err):
			statuses.resolved = conditionState{status: metav1.ConditionFalse, reason: "RefNotFound", message: "Parent reference was not found"}
		case err != nil:
			statuses.resolved = unavailable("Parent reference observation is unavailable")
			statuses.programmed = unavailable("Route programming observation is unavailable")
			statuses.ready = unavailable("Protected route observation is unavailable")
		default:
			statuses = evaluateGateway(route, gateway, statuses)
		}
	}
	conditions := statuses.conditions(route.Status.Conditions, route.Generation, r.now())
	return wayv1.VPNEgressRouteStatus{
		ObservedGeneration: route.Generation,
		Conditions:         conditions,
		Parents: []wayv1.RouteParentStatus{{
			ParentRef: parent, ControllerName: RouteControllerName, Conditions: append(wayv1.Conditions(nil), conditions...),
		}},
	}
}

func evaluateGateway(route *wayv1.VPNEgressRoute, gateway *wayv1.VPNGateway, statuses routeConditionSet) routeConditionSet {
	statuses.resolved = conditionState{status: metav1.ConditionTrue, reason: "ResolvedRefs", message: "Parent reference is resolved"}
	if gateway.Status.ObservedGeneration != gateway.Generation {
		statuses.accepted = unavailable("Parent acceptance observation is unavailable")
		statuses.programmed = unavailable("Parent programming observation is unavailable")
		statuses.ready = unavailable("Parent readiness observation is unavailable")
		return statuses
	}
	accepted := currentCondition(gateway, wayv1.ConditionAccepted)
	if accepted == nil || accepted.Status == metav1.ConditionUnknown {
		statuses.accepted = unavailable("Parent acceptance observation is unavailable")
		statuses.programmed = unavailable("Parent programming observation is unavailable")
		statuses.ready = unavailable("Parent readiness observation is unavailable")
		return statuses
	}
	if accepted.Status != metav1.ConditionTrue {
		statuses.accepted = conditionState{status: metav1.ConditionFalse, reason: "UnsupportedClass", message: "Parent gateway is not accepted"}
		return statuses
	}
	supported := make(map[wayv1.FeatureName]struct{}, len(gateway.Status.SupportedFeatures))
	for _, feature := range gateway.Status.SupportedFeatures {
		supported[feature] = struct{}{}
	}
	for _, feature := range route.Spec.RequiredFeatures {
		if _, ok := supported[feature]; !ok {
			statuses.accepted = conditionState{status: metav1.ConditionFalse, reason: "UnsupportedFeature", message: "A required route feature is unavailable"}
			return statuses
		}
	}
	programmed := currentCondition(gateway, wayv1.ConditionProgrammed)
	if programmed == nil || programmed.Status == metav1.ConditionUnknown {
		statuses.programmed = unavailable("Parent programming observation is unavailable")
		statuses.ready = unavailable("Parent readiness observation is unavailable")
		return statuses
	}
	if programmed.Status != metav1.ConditionTrue {
		reason := programmed.Reason
		if reason != "ApplyFailed" && reason != "StaleGeneration" {
			reason = "Pending"
		}
		statuses.programmed = conditionState{status: metav1.ConditionFalse, reason: reason, message: "Parent gateway is not programmed"}
		return statuses
	}
	statuses.programmed = conditionState{status: metav1.ConditionTrue, reason: "Programmed", message: "Parent gateway is programmed"}
	ready := currentCondition(gateway, wayv1.ConditionReady)
	if ready == nil || ready.Status == metav1.ConditionUnknown {
		statuses.ready = unavailable("Parent readiness observation is unavailable")
		return statuses
	}
	if ready.Status != metav1.ConditionTrue {
		statuses.ready = conditionState{status: metav1.ConditionFalse, reason: "NotReady", message: "Parent gateway is not ready"}
		return statuses
	}
	statuses.ready = conditionState{status: metav1.ConditionTrue, reason: "Ready", message: "Protected route is ready"}
	return statuses
}

func currentCondition(gateway *wayv1.VPNGateway, conditionType string) *metav1.Condition {
	condition := apiMeta.FindStatusCondition(gateway.Status.Conditions, conditionType)
	if condition == nil || condition.ObservedGeneration != gateway.Generation {
		return nil
	}
	return condition
}

type conditionState struct {
	status  metav1.ConditionStatus
	reason  string
	message string
}

type routeConditionSet struct {
	accepted   conditionState
	resolved   conditionState
	programmed conditionState
	ready      conditionState
}

func unavailable(message string) conditionState {
	return conditionState{status: metav1.ConditionUnknown, reason: "ObservationUnavailable", message: message}
}

func (s routeConditionSet) conditions(previous wayv1.Conditions, generation int64, now time.Time) wayv1.Conditions {
	values := []struct {
		typeName string
		state    conditionState
	}{
		{wayv1.ConditionAccepted, s.accepted},
		{wayv1.ConditionResolvedRefs, s.resolved},
		{wayv1.ConditionProgrammed, s.programmed},
		{wayv1.ConditionReady, s.ready},
	}
	conditions := make(wayv1.Conditions, 0, len(values))
	for _, value := range values {
		transition := metav1.NewTime(now)
		if old := apiMeta.FindStatusCondition(previous, value.typeName); old != nil && old.Status == value.state.status {
			transition = old.LastTransitionTime
		}
		conditions = append(conditions, metav1.Condition{Type: value.typeName, Status: value.state.status, Reason: value.state.reason, Message: value.state.message, ObservedGeneration: generation, LastTransitionTime: transition})
	}
	return conditions
}

func (r *VPNEgressRouteReconciler) SetupWithManager(manager ctrl.Manager) error {
	if r.Client == nil {
		r.Client = manager.GetClient()
	}
	if r.Scheme == nil {
		r.Scheme = manager.GetScheme()
	}
	if err := manager.GetFieldIndexer().IndexField(context.Background(), &wayv1.VPNEgressRoute{}, routeParentIndex, func(object client.Object) []string {
		route := object.(*wayv1.VPNEgressRoute)
		if len(route.Spec.ParentRefs) != 1 {
			return nil
		}
		return []string{parentKey(route.Spec.ParentRefs[0])}
	}); err != nil {
		return fmt.Errorf("index route parent references: %w", err)
	}
	return ctrl.NewControllerManagedBy(manager).
		For(&wayv1.VPNEgressRoute{}).
		Watches(&wayv1.VPNGateway{}, handler.EnqueueRequestsFromMapFunc(r.routesForGateway)).
		Complete(r)
}

func (r *VPNEgressRouteReconciler) routesForGateway(ctx context.Context, object client.Object) []reconcile.Request {
	routes := &wayv1.VPNEgressRouteList{}
	if err := r.List(ctx, routes, client.MatchingFields{routeParentIndex: object.GetNamespace() + "/" + object.GetName()}); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(routes.Items))
	for i := range routes.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&routes.Items[i])})
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].NamespacedName.String() < requests[j].NamespacedName.String()
	})
	return requests
}

func parentKey(parent wayv1.GatewayParentReference) string {
	return string(parent.Namespace) + "/" + string(parent.Name)
}

func (r *VPNEgressRouteReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

var _ reconcile.Reconciler = (*VPNEgressRouteReconciler)(nil)
