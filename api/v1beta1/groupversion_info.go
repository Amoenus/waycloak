// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package v1beta1 contains the clean-break Waycloak replacement API.
// +kubebuilder:object:generate=true
// +groupName=networking.waycloak.io
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: "v1beta1"}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		GroupVersion,
		&VPNGatewayClass{}, &VPNGatewayClassList{},
		&VPNGateway{}, &VPNGatewayList{},
		&VPNEgressRoute{}, &VPNEgressRouteList{},
		&VPNWorkloadBinding{}, &VPNWorkloadBindingList{},
		&PortForwardLease{}, &PortForwardLeaseList{},
		&WorkloadAdapter{}, &WorkloadAdapterList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
