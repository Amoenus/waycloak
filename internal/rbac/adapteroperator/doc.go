// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package adapteroperator is the source for the adapter-operator persona role.
// A namespace RoleBinding limits these permissions to that workload namespace.
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=workloadadapters,verbs=get;list;watch;create;update;patch;delete
package adapteroperator
