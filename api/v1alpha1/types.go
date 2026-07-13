// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
)

type NamespacedNameReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}
type EngineSpec struct {
	Type  string `json:"type"`
	Image string `json:"image,omitempty"`
}
type ProviderSpec struct {
	Name                 string                      `json:"name"`
	Protocol             string                      `json:"protocol,omitempty"`
	Region               string                      `json:"region,omitempty"`
	CredentialsSecretRef corev1.LocalObjectReference `json:"credentialsSecretRef"`
}
type OverlaySpec struct {
	// +kubebuilder:validation:Format=cidr
	CIDR string `json:"cidr"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16777215
	VNI int32 `json:"vni"`
	// +kubebuilder:default=1320
	// +kubebuilder:validation:Minimum=576
	MTU int32 `json:"mtu,omitempty"`
}
type DNSSpec struct {
	Mode string `json:"mode,omitempty"`
}
type ClusterTrafficSpec struct {
	Mode string `json:"mode,omitempty"`
}
type PortForwardingSpec struct {
	Enabled bool   `json:"enabled,omitempty"`
	Driver  string `json:"driver,omitempty"`
}
type WorkloadAccessSpec struct {
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
}

type VPNGatewaySpec struct {
	Engine         EngineSpec         `json:"engine"`
	Provider       ProviderSpec       `json:"provider"`
	Overlay        OverlaySpec        `json:"overlay"`
	DNS            DNSSpec            `json:"dns,omitempty"`
	ClusterTraffic ClusterTrafficSpec `json:"clusterTraffic,omitempty"`
	PortForwarding PortForwardingSpec `json:"portForwarding,omitempty"`
	WorkloadAccess WorkloadAccessSpec `json:"workloadAccess"`
}
type VPNGatewayStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	ClientCount        int32              `json:"clientCount,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vpngw
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=='Accepted')].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
type VPNGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VPNGatewaySpec   `json:"spec"`
	Status            VPNGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VPNGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VPNGateway `json:"items"`
}

type PodReference struct {
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}
type VPNWorkloadSpec struct {
	PodRef     PodReference            `json:"podRef"`
	GatewayRef NamespacedNameReference `json:"gatewayRef"`
}
type AllocationStatus struct {
	Address    string       `json:"address,omitempty"`
	Generation int64        `json:"generation,omitempty"`
	ReleasedAt *metav1.Time `json:"releasedAt,omitempty"`
}
type VPNWorkloadStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Allocation         AllocationStatus   `json:"allocation,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// VPNWorkload is controller-owned. Users must not author it.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vpnlw
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.allocation.address`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
type VPNWorkload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VPNWorkloadSpec   `json:"spec"`
	Status            VPNWorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VPNWorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VPNWorkload `json:"items"`
}
