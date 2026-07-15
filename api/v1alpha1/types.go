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
	Type   string                  `json:"type"`
	Image  string                  `json:"image,omitempty"`
	Config *EngineNativeConfigSpec `json:"config,omitempty"`
}

type EngineNativeConfigSpec struct {
	// EnvFrom imports engine-native non-secret environment variables. Waycloak
	// validates reserved integration keys but never copies values into the Pod specification.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	EnvFrom []corev1.LocalObjectReference `json:"envFrom"`
	// Files mounts operator-owned ConfigMaps or Secrets only into the engine.
	// +kubebuilder:validation:MaxItems=8
	Files []EngineFileSource `json:"files,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.configMapRef) != has(self.secretRef)",message="exactly one of configMapRef or secretRef is required"
type EngineFileSource struct {
	ConfigMapRef *corev1.LocalObjectReference `json:"configMapRef,omitempty"`
	SecretRef    *corev1.LocalObjectReference `json:"secretRef,omitempty"`
	// +kubebuilder:validation:Pattern=`^/.*`
	MountPath string `json:"mountPath"`
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

// +kubebuilder:validation:XValidation:rule="!has(self.mode) || self.mode != 'Preserve' || (has(self.cidrs) && size(self.cidrs) > 0)",message="Preserve mode requires at least one cluster CIDR"
// +kubebuilder:validation:XValidation:rule="!has(self.cidrs) || size(self.cidrs) == 0 || (has(self.mode) && self.mode == 'Preserve')",message="cluster CIDRs are only valid in Preserve mode"
type ClusterTrafficSpec struct {
	// +kubebuilder:validation:Enum=Preserve;Gateway;Deny
	Mode string `json:"mode,omitempty"`
	// CIDRs remain on the CNI main routing table in Preserve mode.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:Format=cidr
	// +kubebuilder:validation:items:MaxLength=18
	// +kubebuilder:validation:XValidation:rule="self.all(c, !c.contains(':'))",message="cluster CIDRs must be IPv4"
	CIDRs []string `json:"cidrs,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!self.enabled || (has(self.driver) && self.driver == 'ProtonNatPmp')",message="enabled port forwarding requires driver ProtonNatPmp"
type PortForwardingSpec struct {
	Enabled bool `json:"enabled,omitempty"`
	// +kubebuilder:validation:Enum=ProtonNatPmp
	Driver string `json:"driver,omitempty"`
}
type WorkloadAccessSpec struct {
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
}

// +kubebuilder:validation:XValidation:rule="has(self.provider) != has(self.engine.config)",message="exactly one of legacy provider or engine.config is required"
type VPNGatewaySpec struct {
	Engine         EngineSpec         `json:"engine"`
	Provider       *ProviderSpec      `json:"provider,omitempty"`
	Overlay        OverlaySpec        `json:"overlay"`
	DNS            DNSSpec            `json:"dns,omitempty"`
	ClusterTraffic ClusterTrafficSpec `json:"clusterTraffic,omitempty"`
	PortForwarding PortForwardingSpec `json:"portForwarding,omitempty"`
	WorkloadAccess WorkloadAccessSpec `json:"workloadAccess"`
}
type VPNGatewayStatus struct {
	ObservedGeneration int64                `json:"observedGeneration,omitempty"`
	ClientCount        int32                `json:"clientCount,omitempty"`
	Overlay            GatewayOverlayStatus `json:"overlay,omitempty"`
	Conditions         []metav1.Condition   `json:"conditions,omitempty"`
}

// GatewayOverlayStatus contains observed data-plane state. Endpoint is an IP
// address and UDP port suitable for netip.ParseAddrPort.
type GatewayOverlayStatus struct {
	Endpoint                    string `json:"endpoint,omitempty"`
	HealthPort                  int32  `json:"healthPort,omitempty"`
	DesiredMembershipGeneration string `json:"desiredMembershipGeneration,omitempty"`
	AppliedMembershipGeneration string `json:"appliedMembershipGeneration,omitempty"`
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

// PortForwardProtocol is an application-neutral transport protocol requested
// from a provider-backed forwarded-port lease.
// +kubebuilder:validation:Enum=TCP;UDP
type PortForwardProtocol string

const (
	PortForwardProtocolTCP PortForwardProtocol = "TCP"
	PortForwardProtocolUDP PortForwardProtocol = "UDP"
)

// +kubebuilder:validation:XValidation:rule="(has(self.podSelector.matchLabels) && size(self.podSelector.matchLabels) > 0) || (has(self.podSelector.matchExpressions) && size(self.podSelector.matchExpressions) > 0)",message="podSelector must not be empty"
type PortForwardTargetSpec struct {
	PodSelector metav1.LabelSelector `json:"podSelector"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
	// ApplicationPortMode controls whether the application listens on the stable
	// target port or must follow the provider-assigned public port.
	// +kubebuilder:default=Fixed
	// +kubebuilder:validation:Enum=Fixed;ProviderAssigned
	ApplicationPortMode string `json:"applicationPortMode,omitempty"`
}

type PortForwardLeaseSpec struct {
	GatewayRef NamespacedNameReference `json:"gatewayRef"`
	Target     PortForwardTargetSpec   `json:"target"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +listType=set
	Protocols []PortForwardProtocol `json:"protocols"`
}

// PortForwardTargetStatus binds gateway rules to an observed Pod UID and its
// persisted overlay allocation. A selector match alone is never sufficient.
type PortForwardTargetStatus struct {
	PodRef         PodReference            `json:"podRef"`
	WorkloadRef    NamespacedNameReference `json:"workloadRef"`
	OverlayAddress string                  `json:"overlayAddress"`
	Port           int32                   `json:"port"`
}

type PortForwardLeaseStatus struct {
	ObservedGeneration int64                    `json:"observedGeneration,omitempty"`
	Target             *PortForwardTargetStatus `json:"target,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ProviderInternalPort int32              `json:"providerInternalPort,omitempty"`
	PublicPort           int32              `json:"publicPort,omitempty"`
	IssuedAt             *metav1.Time       `json:"issuedAt,omitempty"`
	RenewAfter           *metav1.Time       `json:"renewAfter,omitempty"`
	ExpiresAt            *metav1.Time       `json:"expiresAt,omitempty"`
	LeaseGeneration      int64              `json:"leaseGeneration,omitempty"`
	Conditions           []metav1.Condition `json:"conditions,omitempty"`
}

// PortForwardLease is user-authored lease intent. Provider allocation,
// observed target binding, gateway rules, and delivery readiness are status.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pflease
// +kubebuilder:printcolumn:name="Public Port",type=integer,JSONPath=`.status.publicPort`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
type PortForwardLease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PortForwardLeaseSpec   `json:"spec"`
	Status            PortForwardLeaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PortForwardLeaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PortForwardLease `json:"items"`
}
