// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package contract

import (
	"crypto/sha256"
	"fmt"
)

const (
	GatewayAnnotation          = "networking.waycloak.io/gateway"
	InjectionVersionAnnotation = "internal.networking.waycloak.io/injection-version"
	AllocationNameAnnotation   = "internal.networking.waycloak.io/allocation-configmap"
	InjectionVersion           = "v1alpha1"
	AllocationVolume           = "waycloak-allocation"
	PrepareContainer           = "waycloak-prepare"
	VerifyContainer            = "waycloak-verify"
	AgentContainer             = "waycloak-agent"
	AgentHealthPort            = 9808
	GatewayDNSPort             = 1053
	WorkloadFinalizer          = "networking.waycloak.io/allocation-quarantine"
)

func AllocationConfigMapName(namespace, pod string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + pod))
	return fmt.Sprintf("waycloak-allocation-%x", sum[:10])
}

func WorkloadName(uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return fmt.Sprintf("pod-%x", sum[:10])
}
