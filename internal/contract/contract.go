// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	GatewayAnnotation              = "networking.waycloak.io/gateway"
	PortForwardContainerAnnotation = "networking.waycloak.io/port-forward-container"
	WorkloadAdapterAnnotation      = "networking.waycloak.io/workload-adapter"
	AdapterContainerAnnotation     = "networking.waycloak.io/adapter-container"
	InjectionVersionAnnotation     = "internal.networking.waycloak.io/injection-version"
	AdmissionGenerationAnnotation  = "internal.networking.waycloak.io/admission-generation"
	AllocationNameAnnotation       = "internal.networking.waycloak.io/allocation-configmap"
	DeliveryDigestAnnotation       = "internal.networking.waycloak.io/port-forward-delivery-digest"
	InjectionVersion               = "v1alpha2"
	AllocationVersion              = "v1alpha1"
	AllocationVolume               = "waycloak-allocation"
	PortForwardVolume              = "waycloak-port-forward"
	PrepareContainer               = "waycloak-prepare"
	VerifyContainer                = "waycloak-verify"
	AgentContainer                 = "waycloak-agent"
	AgentHealthPort                = 9808
	AgentLeasePort                 = 9809
	QBittorrentAdapterHealthPort   = 9810
	BitmagnetAdapterHealthPort     = 9811
	PortForwardLeasesKey           = "port-forward-leases.json"
	AdmissionGenerationKey         = "generation"
	ApplicationLeaseMountPath      = "/run/waycloak/port-forward"
	AdapterProtocolVersion         = "networking.waycloak.io/adapter/v1alpha1"
	AdapterProtocolEnv             = "WAYCLOAK_ADAPTER_PROTOCOL"
	AdapterLeaseEndpointEnv        = "WAYCLOAK_LEASE_ENDPOINT"
	GatewayDNSPort                 = 1053
	WorkloadFinalizer              = "networking.waycloak.io/allocation-quarantine"
	PortForwardLeaseFinalizer      = "networking.waycloak.io/provider-lease-quarantine"
)

func AllocationConfigMapName(namespace, identity string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + identity))
	return fmt.Sprintf("waycloak-allocation-%x", sum[:10])
}

func IsAllocationConfigMapName(name string) bool {
	const prefix = "waycloak-allocation-"
	encoded := strings.TrimPrefix(name, prefix)
	if encoded == name || len(encoded) != 20 {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func DeliveryDigest(document string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(document)))
}

func WorkloadName(uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return fmt.Sprintf("pod-%x", sum[:10])
}
