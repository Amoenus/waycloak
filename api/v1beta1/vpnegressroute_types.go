// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type GatewayParentReference struct {
	// +kubebuilder:validation:Enum=networking.waycloak.io
	// +required
	Group string `json:"group"`
	// +kubebuilder:validation:Enum=VPNGateway
	// +required
	Kind string `json:"kind"`
	// +required
	Namespace NamespaceName `json:"namespace"`
	// +required
	Name ObjectName `json:"name"`
}

type VPNEgressRouteSpec struct {
	// +listType=map
	// +listMapKey=group
	// +listMapKey=kind
	// +listMapKey=namespace
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	// +required
	ParentRefs []GatewayParentReference `json:"parentRefs"`
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +optional
	RequiredFeatures []FeatureName `json:"requiredFeatures,omitempty"`
}

type RouteParentStatus struct {
	// +required
	ParentRef GatewayParentReference `json:"parentRef"`
	// +required
	ControllerName ControllerName `json:"controllerName"`
	// +optional
	Conditions Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.parents) || size(oldSelf.parents) == 0 || (has(self.parents) && size(self.parents) == 1 && (self.parents[0].parentRef != oldSelf.parents[0].parentRef || self.parents[0].controllerName == oldSelf.parents[0].controllerName))",message="controllerName is immutable for a parent and an assigned parent status cannot be cleared"
type VPNEgressRouteStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Conditions Conditions `json:"conditions,omitempty"`
	// ParentRef is an object, so this list cannot use it as a Kubernetes map-list key.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1
	// +optional
	Parents []RouteParentStatus `json:"parents,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRefs[0].name`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=='Accepted')].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type VPNEgressRoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec VPNEgressRouteSpec `json:"spec"`
	// +optional
	Status VPNEgressRouteStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VPNEgressRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VPNEgressRoute `json:"items"`
}
