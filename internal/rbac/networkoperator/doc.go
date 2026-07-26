// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package networkoperator is the source for the network-operator persona role.
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways,verbs=get;list;watch;create;update;patch;delete
package networkoperator
