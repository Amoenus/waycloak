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
	wayconditions "github.com/Amoenus/waycloak/internal/conditions"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	GluetunEnvironmentRole wayv1.QualifiedName = "networking.waycloak.io/GluetunEnvironment"
	OpenVPNCredentialsRole wayv1.QualifiedName = "networking.waycloak.io/OpenVPNCredentials"
)

type ReplacementVPNGatewayReconciler struct {
	client.Client
	APIReader              client.Reader
	ControllerName         wayv1.ControllerName
	ReleaseIdentity        wayv1.ReleaseIdentity
	ConformanceProfile     wayv1.QualifiedName
	SupportedFeatures      []wayv1.FeatureName
	NativeConfigRoles      []wayv1.QualifiedName
	CredentialRoles        []wayv1.QualifiedName
	ReferenceCheckInterval time.Duration
	Now                    func() time.Time
	Runtime                GatewayRuntimeProvisioner
}

type GatewayRuntimeObservation struct {
	Programmed        bool
	Ready             bool
	TunnelReady       bool
	DNSReady          bool
	MembershipApplied bool
	Addresses         []wayv1.GatewayAddress
}

type GatewayRuntimeProvisioner interface {
	Reconcile(context.Context, *wayv1.VPNGateway) (GatewayRuntimeObservation, error)
}

func (r *ReplacementVPNGatewayReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	gateway := &wayv1.VPNGateway{}
	if err := r.Get(ctx, request.NamespacedName, gateway); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	desired := r.desiredStatus(ctx, gateway)
	if !reflect.DeepEqual(gateway.Status, desired) {
		if err := r.applyStatus(ctx, gateway, desired); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: r.referenceCheckInterval()}, nil
}

func (r *ReplacementVPNGatewayReconciler) desiredStatus(ctx context.Context, gateway *wayv1.VPNGateway) wayv1.VPNGatewayStatus {
	status := wayv1.VPNGatewayStatus{ObservedGeneration: gateway.Generation}
	states := gatewayPendingStates()
	class := &wayv1.VPNGatewayClass{}
	if err := r.reader().Get(ctx, client.ObjectKey{Name: string(gateway.Spec.GatewayClassName)}, class); err != nil {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedClass, "Gateway class is unavailable")
		states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonRefNotFound, "Gateway class reference is unresolved")
		return r.finishStatus(gateway, status, states)
	}
	status.GatewayClass = &wayv1.ObservedGatewayClass{ControllerName: class.Spec.ControllerName, ReleaseIdentity: class.Spec.ReleaseIdentity}
	if !class.DeletionTimestamp.IsZero() {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedClass, "Gateway class is deleting")
		states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonIncompatibleRef, "Gateway class is not currently available")
		return r.finishStatus(gateway, status, states)
	}
	if class.Spec.ControllerName != r.ControllerName {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonControllerNotFound, "Gateway class belongs to another controller")
		states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonIncompatibleRef, "Gateway class controller is incompatible")
		return r.finishStatus(gateway, status, states)
	}
	if class.Spec.ReleaseIdentity != r.ReleaseIdentity || class.Spec.ConformanceProfile != r.ConformanceProfile {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedClass, "Gateway class release or conformance identity is unsupported")
		states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonIncompatibleRef, "Gateway class release identity is incompatible")
		return r.finishStatus(gateway, status, states)
	}
	if !sameFeatures(class.Spec.SupportedFeatures, r.SupportedFeatures) {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedFeature, "Gateway class feature set does not match the release")
		states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonIncompatibleRef, "Gateway class feature set is incompatible")
		return r.finishStatus(gateway, status, states)
	}
	if !wayconditions.CurrentTrue(class.Status.Conditions, wayv1.ConditionAccepted, class.Status.ObservedGeneration, class.Generation) ||
		!wayconditions.CurrentTrue(class.Status.Conditions, wayv1.ConditionReady, class.Status.ObservedGeneration, class.Generation) {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonControllerNotFound, "Gateway class controller is not currently available")
		states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonIncompatibleRef, "Gateway class is not currently available")
		return r.finishStatus(gateway, status, states)
	}
	status.SupportedFeatures = sortedFeatures(r.SupportedFeatures)
	if unsupported := firstUnsupported(gateway.Spec.RequestedFeatures, status.SupportedFeatures); unsupported != "" {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedFeature, "Gateway requests a feature unsupported by its class")
		states[wayv1.ConditionResolvedRefs] = wayconditions.True(wayv1.ReasonResolvedRefs, "Gateway class reference is resolved")
		return r.finishStatus(gateway, status, states)
	}
	states[wayv1.ConditionAccepted] = wayconditions.True(wayv1.ReasonAccepted, "Gateway intent is accepted")
	if state, unresolved := r.resolveInputs(ctx, gateway); unresolved {
		states[wayv1.ConditionResolvedRefs] = state
		return r.finishStatus(gateway, status, states)
	}
	states[wayv1.ConditionResolvedRefs] = wayconditions.True(wayv1.ReasonResolvedRefs, "Gateway references are resolved")
	if r.Runtime != nil {
		observation, err := r.Runtime.Reconcile(ctx, gateway)
		if err != nil {
			states[wayv1.ConditionProgrammed] = wayconditions.False(wayv1.ReasonPending, "Gateway runtime reconciliation is pending")
			states[wayv1.ConditionReady] = wayconditions.False(wayv1.ReasonNotReady, "Gateway data plane is not ready")
			return r.finishStatus(gateway, status, states)
		}
		status.Addresses = append([]wayv1.GatewayAddress(nil), observation.Addresses...)
		if observation.Programmed {
			states[wayv1.ConditionProgrammed] = wayconditions.True(wayv1.ReasonProgrammed, "Gateway runtime is programmed")
		}
		if observation.TunnelReady {
			states[wayv1.ConditionTunnelReady] = wayconditions.True(wayv1.ReasonReady, "Gateway tunnel is ready")
		}
		if observation.DNSReady {
			states[wayv1.ConditionDNSReady] = wayconditions.True(wayv1.ReasonReady, "Gateway DNS path is ready")
		}
		if observation.MembershipApplied {
			states[wayv1.ConditionMembershipApplied] = wayconditions.True(wayv1.ReasonProgrammed, "Gateway membership data plane is applied")
		}
		if observation.Ready && observation.Programmed && observation.TunnelReady && observation.DNSReady && observation.MembershipApplied {
			states[wayv1.ConditionReady] = wayconditions.True(wayv1.ReasonReady, "Gateway live data plane is ready")
		}
	}
	return r.finishStatus(gateway, status, states)
}

func gatewayPendingStates() map[string]wayconditions.State {
	return map[string]wayconditions.State{
		wayv1.ConditionAccepted:          wayconditions.False(wayv1.ReasonUnsupportedClass, "Gateway is not accepted"),
		wayv1.ConditionResolvedRefs:      wayconditions.False(wayv1.ReasonRefNotFound, "Gateway references are unresolved"),
		wayv1.ConditionProgrammed:        wayconditions.False(wayv1.ReasonPending, "Gateway data-plane programming has not started"),
		wayv1.ConditionReady:             wayconditions.False(wayv1.ReasonNotReady, "Gateway data plane is not ready"),
		wayv1.ConditionTunnelReady:       wayconditions.False(wayv1.ReasonTunnelNotReady, "Gateway tunnel is not ready"),
		wayv1.ConditionDNSReady:          wayconditions.False(wayv1.ReasonDNSNotReady, "Gateway DNS path is not ready"),
		wayv1.ConditionMembershipApplied: wayconditions.False(wayv1.ReasonMembershipPending, "Gateway membership is not applied"),
	}
}

func (r *ReplacementVPNGatewayReconciler) finishStatus(gateway *wayv1.VPNGateway, status wayv1.VPNGatewayStatus, states map[string]wayconditions.State) wayv1.VPNGatewayStatus {
	order := []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady, wayv1.ConditionTunnelReady, wayv1.ConditionDNSReady, wayv1.ConditionMembershipApplied}
	status.Conditions = wayv1.GatewayConditions(wayconditions.Build(gateway.Status.Conditions, gateway.Generation, r.now(), order, states))
	return status
}

func (r *ReplacementVPNGatewayReconciler) resolveInputs(ctx context.Context, gateway *wayv1.VPNGateway) (wayconditions.State, bool) {
	for _, ref := range gateway.Spec.NativeConfigRefs {
		if !hasRole(r.NativeConfigRoles, ref.Role) {
			return wayconditions.False(wayv1.ReasonIncompatibleRef, "Gateway native configuration role is unsupported"), true
		}
		object := &corev1.ConfigMap{}
		if err := r.reader().Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: string(ref.Name)}, object); err != nil {
			return unresolvedReference(err, "Gateway native configuration reference is unavailable"), true
		}
	}
	for _, ref := range gateway.Spec.CredentialRefs {
		if !hasRole(r.CredentialRoles, ref.Role) {
			return wayconditions.False(wayv1.ReasonIncompatibleRef, "Gateway credential role is unsupported"), true
		}
		object := &metav1.PartialObjectMetadata{}
		object.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
		if err := r.reader().Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: string(ref.Name)}, object); err != nil {
			return unresolvedReference(err, "Gateway credential reference is unavailable"), true
		}
	}
	return wayconditions.State{}, false
}

func unresolvedReference(err error, message string) wayconditions.State {
	if apierrors.IsNotFound(err) {
		return wayconditions.False(wayv1.ReasonRefNotFound, message)
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return wayconditions.False(wayv1.ReasonRefNotPermitted, message)
	}
	return wayconditions.Unknown("Gateway reference observation is unavailable")
}

func (r *ReplacementVPNGatewayReconciler) applyStatus(ctx context.Context, gateway *wayv1.VPNGateway, status wayv1.VPNGatewayStatus) error {
	apply := &wayv1.VPNGateway{TypeMeta: metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNGateway"}, ObjectMeta: metav1.ObjectMeta{Name: gateway.Name, Namespace: gateway.Namespace}, Status: status}
	data, err := json.Marshal(apply)
	if err != nil {
		return fmt.Errorf("encode VPNGateway status apply: %w", err)
	}
	if err := r.SubResource("status").Patch(ctx, apply, client.RawPatch(types.ApplyPatchType, data), client.FieldOwner(wayv1.FieldManagerGatewayController)); err != nil {
		return fmt.Errorf("apply VPNGateway status: %w", err)
	}
	return nil
}

func (r *ReplacementVPNGatewayReconciler) SetupWithManager(manager ctrl.Manager) error {
	if r.Client == nil {
		r.Client = manager.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = manager.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(manager).For(&wayv1.VPNGateway{}).
		Watches(&wayv1.VPNGatewayClass{}, handler.EnqueueRequestsFromMapFunc(r.gatewaysForClass)).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.gatewaysForConfigMap)).Complete(r)
}

func (r *ReplacementVPNGatewayReconciler) gatewaysForClass(ctx context.Context, object client.Object) []reconcile.Request {
	class, ok := object.(*wayv1.VPNGatewayClass)
	if !ok {
		return nil
	}
	return r.matchingGateways(ctx, func(gateway *wayv1.VPNGateway) bool { return string(gateway.Spec.GatewayClassName) == class.Name })
}

func (r *ReplacementVPNGatewayReconciler) gatewaysForConfigMap(ctx context.Context, object client.Object) []reconcile.Request {
	return r.matchingGateways(ctx, func(gateway *wayv1.VPNGateway) bool {
		if gateway.Namespace != object.GetNamespace() {
			return false
		}
		for _, ref := range gateway.Spec.NativeConfigRefs {
			if string(ref.Name) == object.GetName() {
				return true
			}
		}
		return false
	})
}

func (r *ReplacementVPNGatewayReconciler) matchingGateways(ctx context.Context, match func(*wayv1.VPNGateway) bool) []reconcile.Request {
	list := &wayv1.VPNGatewayList{}
	if err := r.List(ctx, list); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for index := range list.Items {
		if match(&list.Items[index]) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[index])})
		}
	}
	return requests
}

func (r *ReplacementVPNGatewayReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *ReplacementVPNGatewayReconciler) referenceCheckInterval() time.Duration {
	if r.ReferenceCheckInterval > 0 {
		return r.ReferenceCheckInterval
	}
	return 10 * time.Second
}

func (r *ReplacementVPNGatewayReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func hasRole(values []wayv1.QualifiedName, expected wayv1.QualifiedName) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func firstUnsupported(requested, supported []wayv1.FeatureName) wayv1.FeatureName {
	for _, feature := range requested {
		if !hasFeature(supported, feature) {
			return feature
		}
	}
	return ""
}

func hasFeature(values []wayv1.FeatureName, expected wayv1.FeatureName) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func sortedFeatures(values []wayv1.FeatureName) []wayv1.FeatureName {
	result := append([]wayv1.FeatureName(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
