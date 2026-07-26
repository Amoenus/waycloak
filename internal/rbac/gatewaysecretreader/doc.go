// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package gatewaysecretreader is the source for the unbound, namespaced
// gateway credential reader role. A RoleBinding grants it only in an approved
// gateway namespace; it must never be bound cluster-wide.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
package gatewaysecretreader
