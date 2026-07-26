// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package nodeagent declares the least-privilege node-agent RBAC contract.
// The agent may observe Pod and binding identity only. It has no Secret,
// Gateway, Route, mutation, finalizer, or status-write authority.
//
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpnworkloadbindings,verbs=get;list;watch
package nodeagent
