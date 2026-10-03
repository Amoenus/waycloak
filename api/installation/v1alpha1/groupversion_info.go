// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package v1alpha1 defines the cluster-administrator installation contract.
// +kubebuilder:object:generate=true
// +groupName=installation.waycloak.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "installation.waycloak.io", Version: "v1alpha1"}
var SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &WaycloakInstallation{}, &WaycloakInstallationList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
})
var AddToScheme = SchemeBuilder.AddToScheme
