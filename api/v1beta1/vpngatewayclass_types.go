// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type ReleaseIdentity struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +required
	Version string `json:"version"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +required
	ManifestDigest string `json:"manifestDigest"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="VPNGatewayClass spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.supportedFeatures.exists(f, f == 'networking.waycloak.io/FailClosedEgress') && self.supportedFeatures.exists(f, f == 'networking.waycloak.io/TCP') && self.supportedFeatures.exists(f, f == 'networking.waycloak.io/UDP') && self.supportedFeatures.exists(f, f == 'networking.waycloak.io/DNSContainment') && self.supportedFeatures.exists(f, f == 'networking.waycloak.io/GatewayReplacementRecovery') && self.supportedFeatures.exists(f, f == 'networking.waycloak.io/NodeRestartRecovery')",message="every class must advertise the frozen baseline feature set"
// +kubebuilder:validation:XValidation:rule="!has(self.parametersRef) || (self.parametersRef.group.size() > 0 && self.parametersRef.kind != 'Secret')",message="parametersRef must target a named non-Secret API group"
type VPNGatewayClassSpec struct {
	// +required
	ControllerName ControllerName `json:"controllerName"`
	// ParametersRef cannot target a Secret and must name a cluster-scoped implementation input.
	// +optional
	ParametersRef *ClusterObjectReference `json:"parametersRef,omitempty"`
	// +required
	ReleaseIdentity ReleaseIdentity `json:"releaseIdentity"`
	// +listType=set
	// +kubebuilder:validation:MinItems=6
	// +kubebuilder:validation:MaxItems=64
	// +required
	SupportedFeatures []FeatureName `json:"supportedFeatures"`
	// +required
	ConformanceProfile QualifiedName `json:"conformanceProfile"`
}

type VPNGatewayClassStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Conditions Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Controller",type=string,JSONPath=`.spec.controllerName`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=='Accepted')].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type VPNGatewayClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec VPNGatewayClassSpec `json:"spec"`
	// +optional
	Status VPNGatewayClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VPNGatewayClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VPNGatewayClass `json:"items"`
}
