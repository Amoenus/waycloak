// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type WorkloadAllocation struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="allocation identity is immutable"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +required
	Identity string `json:"identity"`
	// +kubebuilder:validation:Format=cidr
	// +required
	Address string `json:"address"`
}

type VPNWorkloadBindingSpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="podRef is immutable"
	// +required
	PodRef LocalUIDReference `json:"podRef"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="routeRef is immutable"
	// +required
	RouteRef LocalUIDReference `json:"routeRef"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="gatewayRef is immutable"
	// +required
	GatewayRef NamespacedUIDReference `json:"gatewayRef"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="nodeName is immutable"
	// +required
	NodeName ObjectName `json:"nodeName"`
	// +required
	Allocation WorkloadAllocation `json:"allocation"`
}

type NodeAgentObservation struct {
	// +required
	NodeName ObjectName `json:"nodeName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +required
	NodeBootID string `json:"nodeBootID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +required
	InstanceID string `json:"instanceID"`
	// +required
	ObservedAt metav1.Time `json:"observedAt"`
}

type VPNWorkloadBindingStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	AppliedGeneration int64 `json:"appliedGeneration,omitempty"`
	// +optional
	ObservedPodUID ObjectUID `json:"observedPodUID,omitempty"`
	// +optional
	ObservedGatewayUID ObjectUID `json:"observedGatewayUID,omitempty"`
	// +optional
	Agent *NodeAgentObservation `json:"agent,omitempty"`
	// +optional
	Conditions Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.spec.podRef.name`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.nodeName`
// +kubebuilder:printcolumn:name="Programmed",type=string,JSONPath=`.status.conditions[?(@.type=='Programmed')].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type VPNWorkloadBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec VPNWorkloadBindingSpec `json:"spec"`
	// +optional
	Status VPNWorkloadBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VPNWorkloadBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VPNWorkloadBinding `json:"items"`
}
