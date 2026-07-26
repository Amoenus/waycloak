// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RoleObjectReference struct {
	// +required
	Role QualifiedName `json:"role"`
	// +required
	Name ObjectName `json:"name"`
}

type RouteNamespaceFrom string

const (
	RouteNamespaceSame     RouteNamespaceFrom = "Same"
	RouteNamespaceSelector RouteNamespaceFrom = "Selector"
	RouteNamespaceAll      RouteNamespaceFrom = "All"
)

// +kubebuilder:validation:XValidation:rule="self.from == 'Selector' ? has(self.selector) : !has(self.selector)",message="selector is required only when from is Selector"
type RouteNamespaces struct {
	// +kubebuilder:default=Same
	// +kubebuilder:validation:Enum=Same;Selector;All
	// +optional
	From RouteNamespaceFrom `json:"from,omitempty"`
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

type AllowedRoutes struct {
	// +optional
	Namespaces RouteNamespaces `json:"namespaces,omitempty"`
}

type ClusterTrafficMode string

const (
	ClusterTrafficBypassCluster ClusterTrafficMode = "BypassCluster"
	ClusterTrafficTunnelAll     ClusterTrafficMode = "TunnelAll"
)

// +kubebuilder:validation:XValidation:rule="self.mode != 'BypassCluster' || (has(self.bypassCIDRs) && size(self.bypassCIDRs) > 0)",message="BypassCluster requires at least one reviewed CIDR"
// +kubebuilder:validation:XValidation:rule="self.mode == 'BypassCluster' || !has(self.bypassCIDRs) || size(self.bypassCIDRs) == 0",message="bypassCIDRs are valid only with BypassCluster"
type ClusterTraffic struct {
	// +kubebuilder:validation:Enum=BypassCluster;TunnelAll
	// +required
	Mode ClusterTrafficMode `json:"mode"`
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:Format=cidr
	// +optional
	BypassCIDRs []string `json:"bypassCIDRs,omitempty"`
}

type DNSMode string

const DNSModeGateway DNSMode = "Gateway"

type DNSConfig struct {
	// +kubebuilder:default=Gateway
	// +kubebuilder:validation:Enum=Gateway
	// +optional
	Mode DNSMode `json:"mode,omitempty"`
}

type GatewayPlacement struct {
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

type VPNGatewaySpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="gatewayClassName is immutable"
	// +required
	GatewayClassName ObjectName `json:"gatewayClassName"`
	// +listType=map
	// +listMapKey=role
	// +kubebuilder:validation:MaxItems=16
	// +optional
	NativeConfigRefs []RoleObjectReference `json:"nativeConfigRefs,omitempty"`
	// +listType=map
	// +listMapKey=role
	// +kubebuilder:validation:MaxItems=16
	// +optional
	CredentialRefs []RoleObjectReference `json:"credentialRefs,omitempty"`
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +optional
	RequestedFeatures []FeatureName `json:"requestedFeatures,omitempty"`
	// +kubebuilder:default:={namespaces:{from:Same}}
	// +optional
	AllowedRoutes AllowedRoutes `json:"allowedRoutes,omitempty"`
	// +required
	ClusterTraffic ClusterTraffic `json:"clusterTraffic"`
	// +kubebuilder:default:={mode:Gateway}
	// +optional
	DNS DNSConfig `json:"dns,omitempty"`
	// +optional
	Placement GatewayPlacement `json:"placement,omitempty"`
}

type ObservedGatewayClass struct {
	// +required
	ControllerName ControllerName `json:"controllerName"`
	// +required
	ReleaseIdentity ReleaseIdentity `json:"releaseIdentity"`
}

type GatewayAddress struct {
	// +required
	Type QualifiedName `json:"type"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Value string `json:"value"`
}

type VPNGatewayStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	GatewayClass *ObservedGatewayClass `json:"gatewayClass,omitempty"`
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +optional
	SupportedFeatures []FeatureName `json:"supportedFeatures,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Addresses []GatewayAddress `json:"addresses,omitempty"`
	// +optional
	Conditions Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.gatewayClassName`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=='Accepted')].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type VPNGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec VPNGatewaySpec `json:"spec"`
	// +optional
	Status VPNGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VPNGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VPNGateway `json:"items"`
}
