// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package controller is the single source for the replacement controller role.
// Node-agent authority is declared separately and is never inherited here.
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngatewayclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnegressroutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnegressroutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloadbindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloadbindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloadbindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=portforwardleases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=portforwardleases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=portforwardleases/finalizers,verbs=update
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=workloadadapters,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=workloadadapters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods;namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
package controller
