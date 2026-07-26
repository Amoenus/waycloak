// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type TransportProtocol string

const (
	ProtocolTCP TransportProtocol = "TCP"
	ProtocolUDP TransportProtocol = "UDP"
)

type ServiceBackendReference struct {
	// +required
	Group APIGroup `json:"group"`
	// +required
	Kind Kind `json:"kind"`
	// +required
	Name ObjectName `json:"name"`
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:validation:XValidation:rule="type(self) == int ? self >= 1 && self <= 65535 : self.matches('^[a-z]([-a-z0-9]*[a-z0-9])?$')",message="port must be a valid service port name or number"
	// +required
	Port intstr.IntOrString `json:"port"`
}

type PortForwardEndpointPolicy string

const EndpointPolicySingleActive PortForwardEndpointPolicy = "SingleActive"

// +kubebuilder:validation:XValidation:rule="self.backendRef.group.size() == 0 && self.backendRef.kind == 'Service'",message="backendRef must target a core Service"
type PortForwardLeaseSpec struct {
	// +required
	GatewayRef NamespacedObjectReference `json:"gatewayRef"`
	// +required
	BackendRef ServiceBackendReference `json:"backendRef"`
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:Enum=TCP;UDP
	// +required
	Protocols []TransportProtocol `json:"protocols"`
	// +kubebuilder:default=SingleActive
	// +kubebuilder:validation:Enum=SingleActive
	// +optional
	EndpointPolicy PortForwardEndpointPolicy `json:"endpointPolicy,omitempty"`
	// +optional
	ApplicationAdapterRef *LocalObjectReference `json:"applicationAdapterRef,omitempty"`
}

type EndpointHandoffPhase string

const (
	EndpointPhaseSelecting EndpointHandoffPhase = "Selecting"
	EndpointPhaseDraining  EndpointHandoffPhase = "Draining"
	EndpointPhaseActive    EndpointHandoffPhase = "Active"
)

type ActiveLeaseEndpoint struct {
	// +required
	ServiceUID ObjectUID `json:"serviceUID"`
	// +required
	EndpointSliceUID ObjectUID `json:"endpointSliceUID"`
	// +required
	PodUID ObjectUID `json:"podUID"`
	// +kubebuilder:validation:Enum=Selecting;Draining;Active
	// +required
	Phase EndpointHandoffPhase `json:"phase"`
}

type ProviderMappingStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	PublicAddress string `json:"publicAddress"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +required
	PublicPort int32 `json:"publicPort"`
	// +required
	ExpiresAt metav1.Time `json:"expiresAt"`
}

type PortForwardLeaseStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ActiveEndpoint *ActiveLeaseEndpoint `json:"activeEndpoint,omitempty"`
	// +optional
	Provider *ProviderMappingStatus `json:"provider,omitempty"`
	// +optional
	HandoffGeneration int64 `json:"handoffGeneration,omitempty"`
	// +optional
	Conditions Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.spec.gatewayRef.name`
// +kubebuilder:printcolumn:name="Backend",type=string,JSONPath=`.spec.backendRef.name`
// +kubebuilder:printcolumn:name="Public Port",type=integer,JSONPath=`.status.provider.publicPort`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PortForwardLease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec PortForwardLeaseSpec `json:"spec"`
	// +optional
	Status PortForwardLeaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PortForwardLeaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PortForwardLease `json:"items"`
}
