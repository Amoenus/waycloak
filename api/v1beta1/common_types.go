// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	GroupName = "networking.waycloak.io"

	ConditionAccepted     = "Accepted"
	ConditionResolvedRefs = "ResolvedRefs"
	ConditionProgrammed   = "Programmed"
	ConditionReady        = "Ready"

	FeatureCoreFailClosedEgress       FeatureName = "networking.waycloak.io/CoreFailClosedEgress"
	FeatureTCP                        FeatureName = "networking.waycloak.io/TCP"
	FeatureUDP                        FeatureName = "networking.waycloak.io/UDP"
	FeatureDNSContainment             FeatureName = "networking.waycloak.io/DNSContainment"
	FeatureGatewayReplacementRecovery FeatureName = "networking.waycloak.io/GatewayReplacementRecovery"
	FeatureNodeRestartRecovery        FeatureName = "networking.waycloak.io/NodeRestartRecovery"
	FeaturePortForwardSingleActive    FeatureName = "networking.waycloak.io/PortForwardServiceSingleActive"
	FeatureWorkloadAdapter            FeatureName = "networking.waycloak.io/WorkloadAdapter"

	FieldManagerClassController   = "waycloak-class-controller"
	FieldManagerGatewayController = "waycloak-gateway-controller"
	FieldManagerRoutePrefix       = "waycloak-route-"
	FieldManagerBindingController = "waycloak-binding-controller"
	FieldManagerLeaseController   = "waycloak-lease-controller"
	FieldManagerAdapterController = "waycloak-adapter-controller"
)

func CoreFeatures() []FeatureName {
	return []FeatureName{
		FeatureCoreFailClosedEgress,
		FeatureTCP,
		FeatureUDP,
		FeatureDNSContainment,
		FeatureGatewayReplacementRecovery,
		FeatureNodeRestartRecovery,
	}
}

// QualifiedName is a Kubernetes qualified name.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?([A-Za-z0-9][-A-Za-z0-9_.]{0,61})?[A-Za-z0-9]$`
type QualifiedName string

// ControllerName identifies one immutable controller implementation.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+/[A-Za-z0-9][A-Za-z0-9/._~-]*$`
type ControllerName string

// APIGroup is empty for core APIs or an RFC 1123 subdomain for named groups.
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
type APIGroup string

// Kind is a Kubernetes API kind.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^[A-Za-z]([-A-Za-z0-9]*[A-Za-z0-9])?$`
type Kind string

// ObjectName is a Kubernetes object name represented as a DNS subdomain.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
type ObjectName string

// NamespaceName is a Kubernetes namespace name.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
type NamespaceName string

// ObjectUID is the exact UID of a Kubernetes object.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=128
// +kubebuilder:validation:Pattern=`^[^[:space:]]+$`
type ObjectUID string

// FeatureName is a stable qualified feature identifier.
type FeatureName QualifiedName

// Conditions implements the common positive-polarity status contract.
// +listType=map
// +listMapKey=type
// +kubebuilder:validation:MaxItems=32
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['Accepted', 'ResolvedRefs', 'Programmed', 'Ready'])",message="condition type is not part of the Waycloak common contract"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Accepted' || c.reason in ['Accepted', 'Invalid', 'UnsupportedClass', 'UnsupportedFeature', 'ControllerNotFound', 'Deleting'])",message="Accepted condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'ResolvedRefs' || c.reason in ['ResolvedRefs', 'InvalidRef', 'RefNotFound', 'RefNotPermitted', 'IncompatibleRef'])",message="ResolvedRefs condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Programmed' || c.reason in ['Programmed', 'Pending', 'ApplyFailed', 'StaleGeneration', 'ObservationUnavailable'])",message="Programmed condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Ready' || c.reason in ['Ready', 'NotReady', 'ObservationUnavailable', 'Deleting'])",message="Ready condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.status != 'Unknown' || c.reason == 'ObservationUnavailable')",message="Unknown conditions must use ObservationUnavailable"
type Conditions []metav1.Condition

type ClusterObjectReference struct {
	// +required
	Group APIGroup `json:"group"`
	// +required
	Kind Kind `json:"kind"`
	// +required
	Name ObjectName `json:"name"`
}

type LocalObjectReference struct {
	// +required
	Name ObjectName `json:"name"`
}

type NamespacedObjectReference struct {
	// +required
	Namespace NamespaceName `json:"namespace"`
	// +required
	Name ObjectName `json:"name"`
}

type NamespacedUIDReference struct {
	// +required
	Namespace NamespaceName `json:"namespace"`
	// +required
	Name ObjectName `json:"name"`
	// +required
	UID ObjectUID `json:"uid"`
}

type LocalUIDReference struct {
	// +required
	Name ObjectName `json:"name"`
	// +required
	UID ObjectUID `json:"uid"`
}
