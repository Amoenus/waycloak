// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	GroupName = "networking.waycloak.io"

	ConditionAccepted          = "Accepted"
	ConditionResolvedRefs      = "ResolvedRefs"
	ConditionProgrammed        = "Programmed"
	ConditionReady             = "Ready"
	ConditionTunnelReady       = "TunnelReady"
	ConditionDNSReady          = "DNSReady"
	ConditionMembershipApplied = "MembershipApplied"
	ConditionNodeReady         = "NodeReady"
	ConditionGatewayRulesReady = "GatewayRulesReady"
	ConditionDelivered         = "Delivered"
	ConditionAcknowledged      = "Acknowledged"

	ReasonAccepted               = "Accepted"
	ReasonInvalid                = "Invalid"
	ReasonUnsupportedClass       = "UnsupportedClass"
	ReasonUnsupportedFeature     = "UnsupportedFeature"
	ReasonControllerNotFound     = "ControllerNotFound"
	ReasonDeleting               = "Deleting"
	ReasonResolvedRefs           = "ResolvedRefs"
	ReasonInvalidRef             = "InvalidRef"
	ReasonRefNotFound            = "RefNotFound"
	ReasonRefNotPermitted        = "RefNotPermitted"
	ReasonIncompatibleRef        = "IncompatibleRef"
	ReasonProgrammed             = "Programmed"
	ReasonPending                = "Pending"
	ReasonApplyFailed            = "ApplyFailed"
	ReasonStaleGeneration        = "StaleGeneration"
	ReasonReady                  = "Ready"
	ReasonNotReady               = "NotReady"
	ReasonObservationUnavailable = "ObservationUnavailable"
	ReasonTunnelReady            = "TunnelReady"
	ReasonTunnelNotReady         = "TunnelNotReady"
	ReasonDNSReady               = "DNSReady"
	ReasonDNSNotReady            = "DNSNotReady"
	ReasonMembershipApplied      = "MembershipApplied"
	ReasonMembershipPending      = "MembershipPending"
	ReasonNodeReady              = "NodeReady"
	ReasonNodeNotReady           = "NodeNotReady"
	ReasonGatewayRulesReady      = "GatewayRulesReady"
	ReasonGatewayRulesPending    = "GatewayRulesPending"
	ReasonDelivered              = "Delivered"
	ReasonDeliveryPending        = "DeliveryPending"
	ReasonAcknowledged           = "Acknowledged"
	ReasonAcknowledgementPending = "AcknowledgementPending"

	FeatureFailClosedEgress           FeatureName = "networking.waycloak.io/FailClosedEgress"
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

	// GatewayAddressOverlayCIDR identifies the controller-observed address pool
	// used for UID-bound workload allocations. It is status, not user-authored
	// configuration: the gateway controller derives it from an explicitly
	// confirmed native configuration reference.
	GatewayAddressOverlayCIDR QualifiedName = "networking.waycloak.io/OverlayCIDR"
)

func CoreFeatures() []FeatureName {
	return []FeatureName{
		FeatureFailClosedEgress,
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
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Accepted' || c.reason in ['Accepted', 'Invalid', 'UnsupportedClass', 'UnsupportedFeature', 'ControllerNotFound', 'Deleting', 'ObservationUnavailable'])",message="Accepted condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'ResolvedRefs' || c.reason in ['ResolvedRefs', 'InvalidRef', 'RefNotFound', 'RefNotPermitted', 'IncompatibleRef', 'ObservationUnavailable'])",message="ResolvedRefs condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Programmed' || c.reason in ['Programmed', 'Pending', 'ApplyFailed', 'StaleGeneration', 'ObservationUnavailable'])",message="Programmed condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Ready' || c.reason in ['Ready', 'NotReady', 'ObservationUnavailable', 'Deleting'])",message="Ready condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.status != 'Unknown' || c.reason == 'ObservationUnavailable')",message="Unknown conditions must use ObservationUnavailable"
type Conditions []metav1.Condition

// GatewayConditions adds the live tunnel, DNS, and membership observations
// required to explain VPNGateway readiness.
// +listType=map
// +listMapKey=type
// +kubebuilder:validation:MaxItems=32
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['Accepted', 'ResolvedRefs', 'Programmed', 'Ready', 'TunnelReady', 'DNSReady', 'MembershipApplied'])",message="condition type is not valid for VPNGateway"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Accepted' || c.reason in ['Accepted', 'Invalid', 'UnsupportedClass', 'UnsupportedFeature', 'ControllerNotFound', 'Deleting', 'ObservationUnavailable'])",message="Accepted condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'ResolvedRefs' || c.reason in ['ResolvedRefs', 'InvalidRef', 'RefNotFound', 'RefNotPermitted', 'IncompatibleRef', 'ObservationUnavailable'])",message="ResolvedRefs condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Programmed' || c.reason in ['Programmed', 'Pending', 'ApplyFailed', 'StaleGeneration', 'ObservationUnavailable'])",message="Programmed condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Ready' || c.reason in ['Ready', 'NotReady', 'ObservationUnavailable', 'Deleting'])",message="Ready condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'TunnelReady' || c.reason in ['TunnelReady', 'TunnelNotReady', 'ObservationUnavailable'])",message="TunnelReady condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'DNSReady' || c.reason in ['DNSReady', 'DNSNotReady', 'ObservationUnavailable'])",message="DNSReady condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'MembershipApplied' || c.reason in ['MembershipApplied', 'MembershipPending', 'ObservationUnavailable'])",message="MembershipApplied condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.status != 'Unknown' || c.reason == 'ObservationUnavailable')",message="Unknown conditions must use ObservationUnavailable"
type GatewayConditions []metav1.Condition

// BindingConditions adds the current node observation required to explain an
// exact Pod UID binding's readiness.
// +listType=map
// +listMapKey=type
// +kubebuilder:validation:MaxItems=32
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['Accepted', 'ResolvedRefs', 'Programmed', 'Ready', 'NodeReady'])",message="condition type is not valid for VPNWorkloadBinding"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Accepted' || c.reason in ['Accepted', 'Invalid', 'UnsupportedClass', 'UnsupportedFeature', 'ControllerNotFound', 'Deleting', 'ObservationUnavailable'])",message="Accepted condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'ResolvedRefs' || c.reason in ['ResolvedRefs', 'InvalidRef', 'RefNotFound', 'RefNotPermitted', 'IncompatibleRef', 'ObservationUnavailable'])",message="ResolvedRefs condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Programmed' || c.reason in ['Programmed', 'Pending', 'ApplyFailed', 'StaleGeneration', 'ObservationUnavailable'])",message="Programmed condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Ready' || c.reason in ['Ready', 'NotReady', 'ObservationUnavailable', 'Deleting'])",message="Ready condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'NodeReady' || c.reason in ['NodeReady', 'NodeNotReady', 'ObservationUnavailable'])",message="NodeReady condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.status != 'Unknown' || c.reason == 'ObservationUnavailable')",message="Unknown conditions must use ObservationUnavailable"
type BindingConditions []metav1.Condition

// LeaseConditions adds the gateway-rule, delivery, and application
// acknowledgement observations required for port-forward lease readiness.
// +listType=map
// +listMapKey=type
// +kubebuilder:validation:MaxItems=32
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['Accepted', 'ResolvedRefs', 'Programmed', 'Ready', 'GatewayRulesReady', 'Delivered', 'Acknowledged'])",message="condition type is not valid for PortForwardLease"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Accepted' || c.reason in ['Accepted', 'Invalid', 'UnsupportedClass', 'UnsupportedFeature', 'ControllerNotFound', 'Deleting', 'ObservationUnavailable'])",message="Accepted condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'ResolvedRefs' || c.reason in ['ResolvedRefs', 'InvalidRef', 'RefNotFound', 'RefNotPermitted', 'IncompatibleRef', 'ObservationUnavailable'])",message="ResolvedRefs condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Programmed' || c.reason in ['Programmed', 'Pending', 'ApplyFailed', 'StaleGeneration', 'ObservationUnavailable'])",message="Programmed condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Ready' || c.reason in ['Ready', 'NotReady', 'ObservationUnavailable', 'Deleting'])",message="Ready condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'GatewayRulesReady' || c.reason in ['GatewayRulesReady', 'GatewayRulesPending', 'ObservationUnavailable'])",message="GatewayRulesReady condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Delivered' || c.reason in ['Delivered', 'DeliveryPending', 'ObservationUnavailable'])",message="Delivered condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.type != 'Acknowledged' || c.reason in ['Acknowledged', 'AcknowledgementPending', 'ObservationUnavailable'])",message="Acknowledged condition reason is not stable"
// +kubebuilder:validation:XValidation:rule="self.all(c, c.status != 'Unknown' || c.reason == 'ObservationUnavailable')",message="Unknown conditions must use ObservationUnavailable"
type LeaseConditions []metav1.Condition

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
