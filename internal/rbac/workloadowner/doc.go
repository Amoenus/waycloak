// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package workloadowner is the source for the workload-owner persona role.
// A namespace RoleBinding limits these permissions to that workload namespace.
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnegressroutes;portforwardleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloadbindings,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=workloadadapters,verbs=get;list;watch
package workloadowner
