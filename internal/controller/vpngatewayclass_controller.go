// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	wayconditions "github.com/Amoenus/waycloak/internal/conditions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const DefaultGatewayControllerName wayv1.ControllerName = "gluetun.waycloak.io/controller"

var releaseDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidReleaseIdentity(identity wayv1.ReleaseIdentity) bool {
	return len(identity.Version) > 0 && len(identity.Version) <= 128 && releaseDigestPattern.MatchString(identity.ManifestDigest)
}

type VPNGatewayClassReconciler struct {
	client.Client
	ControllerName     wayv1.ControllerName
	ReleaseIdentity    wayv1.ReleaseIdentity
	ConformanceProfile wayv1.QualifiedName
	SupportedFeatures  []wayv1.FeatureName
	Now                func() time.Time
}

func (r *VPNGatewayClassReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	class := &wayv1.VPNGatewayClass{}
	if err := r.Get(ctx, request.NamespacedName, class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if class.Spec.ControllerName != r.ControllerName {
		return ctrl.Result{}, nil
	}
	desired := r.desiredStatus(class)
	if reflect.DeepEqual(class.Status, desired) {
		return ctrl.Result{}, nil
	}
	apply := &wayv1.VPNGatewayClass{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNGatewayClass"},
		ObjectMeta: metav1.ObjectMeta{Name: class.Name}, Status: desired,
	}
	data, err := json.Marshal(apply)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("encode VPNGatewayClass status apply: %w", err)
	}
	if err := r.SubResource("status").Patch(ctx, apply, client.RawPatch(types.ApplyPatchType, data), client.FieldOwner(wayv1.FieldManagerClassController)); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply VPNGatewayClass status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *VPNGatewayClassReconciler) desiredStatus(class *wayv1.VPNGatewayClass) wayv1.VPNGatewayClassStatus {
	states := map[string]wayconditions.State{
		wayv1.ConditionAccepted:     wayconditions.True(wayv1.ReasonAccepted, "Gateway class is accepted"),
		wayv1.ConditionResolvedRefs: wayconditions.True(wayv1.ReasonResolvedRefs, "Gateway class references are resolved"),
		wayv1.ConditionProgrammed:   wayconditions.True(wayv1.ReasonProgrammed, "Gateway class is available to the controller"),
		wayv1.ConditionReady:        wayconditions.True(wayv1.ReasonReady, "Gateway class release and profile are available"),
	}
	if class.Spec.ReleaseIdentity != r.ReleaseIdentity || class.Spec.ConformanceProfile != r.ConformanceProfile {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedClass, "Gateway class release or conformance identity is unsupported")
		states[wayv1.ConditionProgrammed] = wayconditions.False(wayv1.ReasonPending, "Unsupported gateway class is not registered")
		states[wayv1.ConditionReady] = wayconditions.False(wayv1.ReasonNotReady, "Gateway class is unavailable")
	} else if !sameFeatures(class.Spec.SupportedFeatures, r.SupportedFeatures) {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedFeature, "Gateway class feature set does not match the release")
		states[wayv1.ConditionProgrammed] = wayconditions.False(wayv1.ReasonPending, "Unsupported gateway class is not registered")
		states[wayv1.ConditionReady] = wayconditions.False(wayv1.ReasonNotReady, "Gateway class is unavailable")
	}
	if class.Spec.ParametersRef != nil {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonUnsupportedClass, "Gateway class parameters are not supported by this release")
		states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonIncompatibleRef, "Gateway class parameters are incompatible")
		states[wayv1.ConditionProgrammed] = wayconditions.False(wayv1.ReasonPending, "Gateway class parameters are not registered")
		states[wayv1.ConditionReady] = wayconditions.False(wayv1.ReasonNotReady, "Gateway class is unavailable")
	}
	if !class.DeletionTimestamp.IsZero() {
		states[wayv1.ConditionAccepted] = wayconditions.False(wayv1.ReasonDeleting, "Gateway class is deleting")
		states[wayv1.ConditionProgrammed] = wayconditions.False(wayv1.ReasonPending, "Deleting gateway class is not available")
		states[wayv1.ConditionReady] = wayconditions.False(wayv1.ReasonNotReady, "Gateway class is unavailable")
	}
	return wayv1.VPNGatewayClassStatus{
		ObservedGeneration: class.Generation,
		Conditions:         wayv1.Conditions(wayconditions.Build(class.Status.Conditions, class.Generation, r.now(), wayconditions.SummaryOrder(), states)),
	}
}

func (r *VPNGatewayClassReconciler) SetupWithManager(manager ctrl.Manager) error {
	if r.Client == nil {
		r.Client = manager.GetClient()
	}
	return ctrl.NewControllerManagedBy(manager).For(&wayv1.VPNGatewayClass{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
		class, ok := object.(*wayv1.VPNGatewayClass)
		return ok && class.Spec.ControllerName == r.ControllerName
	}))).Complete(r)
}

func sameFeatures(left, right []wayv1.FeatureName) bool {
	if len(left) != len(right) {
		return false
	}
	a := append([]wayv1.FeatureName(nil), left...)
	b := append([]wayv1.FeatureName(nil), right...)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return reflect.DeepEqual(a, b)
}

func (r *VPNGatewayClassReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
