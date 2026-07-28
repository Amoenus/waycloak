// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	wayconditions "github.com/Amoenus/waycloak/internal/conditions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var replacementRelease = wayv1.ReleaseIdentity{
	Version:        "v1.0.0-beta.1",
	ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
}

func TestGatewayClassPublishesReadyOnlyForExactReleaseProfileAndFeatures(t *testing.T) {
	class := replacementClass()
	reconciler := &VPNGatewayClassReconciler{
		ControllerName: DefaultGatewayControllerName, ReleaseIdentity: replacementRelease,
		ConformanceProfile: "networking.waycloak.io/Core-v1", SupportedFeatures: wayv1.CoreFeatures(),
		Now: func() time.Time { return time.Unix(1000, 0).UTC() },
	}
	status := reconciler.desiredStatus(class)
	for _, conditionType := range wayconditions.SummaryOrder() {
		if !wayconditions.CurrentTrue(status.Conditions, conditionType, status.ObservedGeneration, class.Generation) {
			t.Fatalf("class condition %s is not current True: %#v", conditionType, status.Conditions)
		}
	}

	class.Spec.ReleaseIdentity.ManifestDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	status = reconciler.desiredStatus(class)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionAccepted, metav1.ConditionFalse, wayv1.ReasonUnsupportedClass)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionProgrammed, metav1.ConditionFalse, wayv1.ReasonPending)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionReady, metav1.ConditionFalse, wayv1.ReasonNotReady)

	class = replacementClass()
	class.Spec.SupportedFeatures = append(class.Spec.SupportedFeatures, wayv1.FeaturePortForwardSingleActive)
	status = reconciler.desiredStatus(class)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionAccepted, metav1.ConditionFalse, wayv1.ReasonUnsupportedFeature)

	class = replacementClass()
	deletingAt := metav1.NewTime(time.Unix(900, 0).UTC())
	class.DeletionTimestamp = &deletingAt
	status = reconciler.desiredStatus(class)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionAccepted, metav1.ConditionFalse, wayv1.ReasonDeleting)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionProgrammed, metav1.ConditionFalse, wayv1.ReasonPending)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionReady, metav1.ConditionFalse, wayv1.ReasonNotReady)
}

func TestGatewayRejectsMissingForeignAndUnsupportedClassBeforeProgramming(t *testing.T) {
	gateway := replacementGateway()
	reconciler := replacementGatewayReconciler(t)
	status := reconciler.desiredStatus(context.Background(), gateway)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionAccepted, metav1.ConditionFalse, wayv1.ReasonUnsupportedClass)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionResolvedRefs, metav1.ConditionFalse, wayv1.ReasonRefNotFound)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionProgrammed, metav1.ConditionFalse, wayv1.ReasonPending)
	if status.GatewayClass != nil || len(status.Addresses) != 0 {
		t.Fatalf("missing-class status disclosed programming state: %#v", status)
	}

	foreign := replacementClass()
	foreign.Spec.ControllerName = "other.waycloak.io/controller"
	reconciler = replacementGatewayReconciler(t, foreign)
	status = reconciler.desiredStatus(context.Background(), gateway)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionAccepted, metav1.ConditionFalse, wayv1.ReasonControllerNotFound)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionResolvedRefs, metav1.ConditionFalse, wayv1.ReasonIncompatibleRef)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionProgrammed, metav1.ConditionFalse, wayv1.ReasonPending)
	if len(status.Addresses) != 0 {
		t.Fatalf("foreign class retained addresses: %#v", status.Addresses)
	}

	mismatched := replacementClass()
	mismatched.Spec.ReleaseIdentity.ManifestDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	reconciler = replacementGatewayReconciler(t, mismatched)
	status = reconciler.desiredStatus(context.Background(), gateway)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionAccepted, metav1.ConditionFalse, wayv1.ReasonUnsupportedClass)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionResolvedRefs, metav1.ConditionFalse, wayv1.ReasonIncompatibleRef)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionProgrammed, metav1.ConditionFalse, wayv1.ReasonPending)

	class := replacementClass()
	gateway.Spec.RequestedFeatures = []wayv1.FeatureName{wayv1.FeaturePortForwardSingleActive}
	reconciler = replacementGatewayReconciler(t, class)
	status = reconciler.desiredStatus(context.Background(), gateway)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionAccepted, metav1.ConditionFalse, wayv1.ReasonUnsupportedFeature)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionResolvedRefs, metav1.ConditionTrue, wayv1.ReasonResolvedRefs)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionProgrammed, metav1.ConditionFalse, wayv1.ReasonPending)
}

func TestMinimalGatewayResolvesWithoutImagesAndCredentialValuesNeverReachStatus(t *testing.T) {
	class := replacementClass()
	gateway := replacementGateway()
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "native", Namespace: gateway.Namespace}, Data: map[string]string{"VPN_SERVICE_PROVIDER": "protonvpn"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: gateway.Namespace}, Data: map[string][]byte{"password": []byte("must-not-appear")}}
	gateway.Spec.NativeConfigRefs = []wayv1.RoleObjectReference{{Role: GluetunEnvironmentRole, Name: "native"}}
	gateway.Spec.CredentialRefs = []wayv1.RoleObjectReference{{Role: OpenVPNCredentialsRole, Name: "credentials"}}
	reconciler := replacementGatewayReconciler(t, class, configMap, secret)
	status := reconciler.desiredStatus(context.Background(), gateway)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionAccepted, metav1.ConditionTrue, wayv1.ReasonAccepted)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionResolvedRefs, metav1.ConditionTrue, wayv1.ReasonResolvedRefs)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionProgrammed, metav1.ConditionFalse, wayv1.ReasonPending)
	if status.GatewayClass == nil || status.GatewayClass.ReleaseIdentity != replacementRelease || len(status.SupportedFeatures) != len(wayv1.CoreFeatures()) {
		t.Fatalf("minimal gateway status = %#v", status)
	}
	if strings.Contains(strings.ToLower(replacementStatusText(status)), "password") || strings.Contains(replacementStatusText(status), "must-not-appear") {
		t.Fatalf("gateway status exposed credential material: %#v", status)
	}

	gateway.Spec.CredentialRefs[0].Name = "missing"
	status = reconciler.desiredStatus(context.Background(), gateway)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionResolvedRefs, metav1.ConditionFalse, wayv1.ReasonRefNotFound)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionProgrammed, metav1.ConditionFalse, wayv1.ReasonPending)
	if len(status.Addresses) != 0 {
		t.Fatalf("unresolved credential retained addresses: %#v", status.Addresses)
	}
}

func TestGatewayReadyRequiresCompleteLiveRuntimeObservation(t *testing.T) {
	class := replacementClass()
	gateway := replacementGateway()
	reconciler := replacementGatewayReconciler(t, class)
	reconciler.Runtime = staticGatewayRuntime{observation: GatewayRuntimeObservation{Programmed: true, Ready: true, TunnelReady: true, DNSReady: true, MembershipApplied: true, Addresses: []wayv1.GatewayAddress{{Type: wayv1.GatewayAddressTypeOverlayCIDR, Value: "100.96.0.0/24"}}}}
	status := reconciler.desiredStatus(context.Background(), gateway)
	for _, conditionType := range []string{wayv1.ConditionProgrammed, wayv1.ConditionTunnelReady, wayv1.ConditionDNSReady, wayv1.ConditionMembershipApplied, wayv1.ConditionReady} {
		assertReplacementCondition(t, status.Conditions, conditionType, metav1.ConditionTrue, map[string]string{wayv1.ConditionProgrammed: wayv1.ReasonProgrammed, wayv1.ConditionTunnelReady: wayv1.ReasonTunnelReady, wayv1.ConditionDNSReady: wayv1.ReasonDNSReady, wayv1.ConditionMembershipApplied: wayv1.ReasonMembershipApplied, wayv1.ConditionReady: wayv1.ReasonReady}[conditionType])
	}
	if len(status.Addresses) != 1 || status.Addresses[0].Value != "100.96.0.0/24" {
		t.Fatalf("runtime addresses not published: %#v", status.Addresses)
	}
	reconciler.Runtime = staticGatewayRuntime{observation: GatewayRuntimeObservation{Programmed: true}}
	status = reconciler.desiredStatus(context.Background(), gateway)
	assertReplacementCondition(t, status.Conditions, wayv1.ConditionReady, metav1.ConditionFalse, wayv1.ReasonNotReady)
}

type staticGatewayRuntime struct {
	observation GatewayRuntimeObservation
	err         error
}

func (runtime staticGatewayRuntime) Reconcile(context.Context, *wayv1.VPNGateway) (GatewayRuntimeObservation, error) {
	return runtime.observation, runtime.err
}

func replacementGatewayReconciler(t *testing.T, objects ...runtime.Object) *ReplacementVPNGatewayReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	return &ReplacementVPNGatewayReconciler{
		Client: client, APIReader: client, ControllerName: DefaultGatewayControllerName,
		ReleaseIdentity: replacementRelease, ConformanceProfile: "networking.waycloak.io/Core-v1", SupportedFeatures: wayv1.CoreFeatures(),
		NativeConfigRoles: []wayv1.QualifiedName{GluetunEnvironmentRole}, CredentialRoles: []wayv1.QualifiedName{OpenVPNCredentialsRole},
		Now: func() time.Time { return time.Unix(1000, 0).UTC() },
	}
}

func replacementClass() *wayv1.VPNGatewayClass {
	class := &wayv1.VPNGatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "gluetun.waycloak.io", Generation: 1}, Spec: wayv1.VPNGatewayClassSpec{
		ControllerName: DefaultGatewayControllerName, ReleaseIdentity: replacementRelease,
		SupportedFeatures: wayv1.CoreFeatures(), ConformanceProfile: "networking.waycloak.io/Core-v1",
	}}
	class.Status = (&VPNGatewayClassReconciler{ControllerName: DefaultGatewayControllerName, ReleaseIdentity: replacementRelease, ConformanceProfile: class.Spec.ConformanceProfile, SupportedFeatures: wayv1.CoreFeatures(), Now: func() time.Time { return time.Unix(1000, 0).UTC() }}).desiredStatus(class)
	return class
}

func replacementGateway() *wayv1.VPNGateway {
	return &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "network", Generation: 2}, Spec: wayv1.VPNGatewaySpec{
		GatewayClassName: "gluetun.waycloak.io", ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
	}}
}

func assertReplacementCondition(t *testing.T, values []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	var condition *metav1.Condition
	for index := range values {
		if values[index].Type == conditionType {
			condition = &values[index]
			break
		}
	}
	if condition == nil || condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = %#v", conditionType, condition)
	}
}

func replacementStatusText(status wayv1.VPNGatewayStatus) string {
	var builder strings.Builder
	for _, condition := range status.Conditions {
		builder.WriteString(condition.Message)
	}
	return builder.String()
}
