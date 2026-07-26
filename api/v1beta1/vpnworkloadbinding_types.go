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

// WorkloadNetworkIntent is the controller-authored, credential-free projection
// consumed by the node agent. It contains no provider or Kubernetes credential.
// The node agent still validates every field and the exact binding generation
// before using it as privileged programming authority.
type WorkloadNetworkIntent struct {
	// +kubebuilder:validation:Minimum=1
	// +required
	GatewayGeneration int64 `json:"gatewayGeneration"`
	// +kubebuilder:validation:Format=cidr
	// +required
	OverlayCIDR string `json:"overlayCIDR"`
	// +kubebuilder:validation:Format=ip
	// +required
	GatewayAddress string `json:"gatewayAddress"`
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=253
	// +required
	GatewayEndpoint string `json:"gatewayEndpoint"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +required
	GatewayHealthPort int32 `json:"gatewayHealthPort"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16777215
	// +required
	VNI int32 `json:"vni"`
	// +kubebuilder:validation:Minimum=576
	// +kubebuilder:validation:Maximum=9000
	// +required
	MTU int32 `json:"mtu"`
	// +required
	ClusterTraffic ClusterTraffic `json:"clusterTraffic"`
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
	// +required
	Network WorkloadNetworkIntent `json:"network"`
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
	Conditions BindingConditions `json:"conditions,omitempty"`
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
