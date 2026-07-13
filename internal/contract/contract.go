// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package contract

import (
	"crypto/sha256"
	"fmt"
)

const (
	GatewayAnnotation              = "networking.waycloak.io/gateway"
	PortForwardContainerAnnotation = "networking.waycloak.io/port-forward-container"
	InjectionVersionAnnotation     = "internal.networking.waycloak.io/injection-version"
	AllocationNameAnnotation       = "internal.networking.waycloak.io/allocation-configmap"
	DeliveryDigestAnnotation       = "internal.networking.waycloak.io/port-forward-delivery-digest"
	InjectionVersion               = "v1alpha1"
	AllocationVolume               = "waycloak-allocation"
	PortForwardVolume              = "waycloak-port-forward"
	PrepareContainer               = "waycloak-prepare"
	VerifyContainer                = "waycloak-verify"
	AgentContainer                 = "waycloak-agent"
	AgentHealthPort                = 9808
	AgentLeasePort                 = 9809
	PortForwardLeasesKey           = "port-forward-leases.json"
	ApplicationLeaseMountPath      = "/run/waycloak/port-forward"
	GatewayDNSPort                 = 1053
	WorkloadFinalizer              = "networking.waycloak.io/allocation-quarantine"
	PortForwardLeaseFinalizer      = "networking.waycloak.io/provider-lease-quarantine"
)

func AllocationConfigMapName(namespace, pod string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + pod))
	return fmt.Sprintf("waycloak-allocation-%x", sum[:10])
}

func DeliveryDigest(document string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(document)))
}

func WorkloadName(uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return fmt.Sprintf("pod-%x", sum[:10])
}
