// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="WorkloadAdapter spec is immutable"
type WorkloadAdapterSpec struct {
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9.-]*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[a-f0-9]{64}$`
	// +required
	Image string `json:"image"`
	// +kubebuilder:validation:Enum=networking.waycloak.io/adapter/v1
	// +required
	ProtocolVersion string `json:"protocolVersion"`
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +required
	SupportedApplications []QualifiedName `json:"supportedApplications"`
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +optional
	SupportedFeatures []FeatureName `json:"supportedFeatures,omitempty"`
}

type WorkloadAdapterStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Conditions Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.protocolVersion`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=='Accepted')].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type WorkloadAdapter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec WorkloadAdapterSpec `json:"spec"`
	// +optional
	Status WorkloadAdapterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WorkloadAdapterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkloadAdapter `json:"items"`
}
