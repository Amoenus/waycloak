// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type PortForwarding struct {
	// Immutable, pre-created controller mTLS Secret in the runtime namespace.
	// +kubebuilder:validation:MinLength=1
	ControllerTLSSecret string `json:"controllerTLSSecret"`
	// +optional
	AdapterEnabled bool `json:"adapterEnabled,omitempty"`
}

// InstallationConfig exposes configuration without allowing release artifact,
// denial, or credential-injection overrides. Omitted settings use chart defaults.
type InstallationConfig struct {
	// +optional
	ControllerResources corev1.ResourceRequirements `json:"controllerResources,omitempty"`
	// +optional
	NodeAgentResources corev1.ResourceRequirements `json:"nodeAgentResources,omitempty"`
	// +optional
	InstallerResources corev1.ResourceRequirements `json:"installerResources,omitempty"`
	// +optional
	ControllerNodeSelector map[string]string `json:"controllerNodeSelector,omitempty"`
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	// +optional
	ControllerTolerations []corev1.Toleration `json:"controllerTolerations,omitempty"`
	// +optional
	PortForwarding *PortForwarding `json:"portForwarding,omitempty"`
}

type WaycloakInstallationSpec struct {
	// Exact published release tag. Updating this field requests an upgrade or rollback.
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
	Version string `json:"version"`
	// +kubebuilder:default=waycloak-system
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="runtime namespace is immutable"
	Namespace string `json:"namespace"`
	// +kubebuilder:default=waycloak
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="runtime release name is immutable"
	Release string `json:"release"`
	// +kubebuilder:default="100.96.0.0/16"
	// +kubebuilder:validation:Format=cidr
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="overlay CIDR requires a separate network migration"
	OverlayCIDR string `json:"overlayCIDR"`
	// Selects the support row on a mixed-architecture cluster; existing node coverage is retained.
	// +kubebuilder:validation:Enum=amd64;arm64
	// +optional
	NodeArchitecture string `json:"nodeArchitecture,omitempty"`
	// +optional
	Config InstallationConfig `json:"config,omitempty"`
	// Adopt an existing Helm release after its previous manager has stopped reconciling it.
	// +optional
	AdoptExisting bool `json:"adoptExisting,omitempty"`
	// Suspend new transitions. An already-journaled transition completes before suspension.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

type WaycloakInstallationStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ReadyGeneration int64 `json:"readyGeneration,omitempty"`
	// +optional
	AppliedVersion string `json:"appliedVersion,omitempty"`
	// +optional
	AppliedManifestDigest string `json:"appliedManifestDigest,omitempty"`
	// +optional
	AppliedIntentDigest string `json:"appliedIntentDigest,omitempty"`
	// +optional
	ActiveGeneration int64 `json:"activeGeneration,omitempty"`
	// +optional
	ActivePlanID string `json:"activePlanID,omitempty"`
	// +optional
	Phase string `json:"phase,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=wci
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'waycloak'",message="one cluster-wide installation named waycloak owns node CNI"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Applied",type=string,JSONPath=`.status.appliedVersion`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type WaycloakInstallation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WaycloakInstallationSpec `json:"spec"`
	// +optional
	Status WaycloakInstallationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WaycloakInstallationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WaycloakInstallation `json:"items"`
}
