// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"net/netip"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

const (
	RuntimeAPIVersion                                        = "port-forward-runtime.waycloak.io/v1"
	ProviderCleanupFinalizer                                 = "networking.waycloak.io/provider-cleanup"
	ProviderAssignedApplicationPortFeature wayv1.FeatureName = "networking.waycloak.io/ProviderAssignedApplicationPort"
)

type ApplicationPortMode string

const (
	ApplicationPortFixed            ApplicationPortMode = "Fixed"
	ApplicationPortProviderAssigned ApplicationPortMode = "ProviderAssigned"
)

type Intent struct {
	APIVersion           string                    `json:"apiVersion"`
	LeaseNamespace       wayv1.NamespaceName       `json:"leaseNamespace"`
	LeaseUID             wayv1.ObjectUID           `json:"leaseUID"`
	GatewayUID           wayv1.ObjectUID           `json:"gatewayUID"`
	HandoffGeneration    int64                     `json:"handoffGeneration"`
	ServiceUID           wayv1.ObjectUID           `json:"serviceUID"`
	EndpointSliceUID     wayv1.ObjectUID           `json:"endpointSliceUID"`
	PodUID               wayv1.ObjectUID           `json:"podUID"`
	BindingUID           wayv1.ObjectUID           `json:"bindingUID"`
	ProviderInternalPort uint16                    `json:"providerInternalPort"`
	PreviousPublicPort   uint16                    `json:"previousPublicPort,omitempty"`
	OverlayAddress       netip.Addr                `json:"overlayAddress"`
	ApplicationAddress   netip.Addr                `json:"applicationAddress"`
	BackendPort          uint16                    `json:"backendPort"`
	TargetPort           uint16                    `json:"targetPort"`
	Protocols            []wayv1.TransportProtocol `json:"protocols"`
	AdapterName          wayv1.ObjectName          `json:"adapterName,omitempty"`
	ApplicationPortMode  ApplicationPortMode       `json:"applicationPortMode"`
}

type Observation struct {
	APIVersion        string               `json:"apiVersion"`
	LeaseUID          wayv1.ObjectUID      `json:"leaseUID"`
	GatewayUID        wayv1.ObjectUID      `json:"gatewayUID"`
	HandoffGeneration int64                `json:"handoffGeneration"`
	PodUID            wayv1.ObjectUID      `json:"podUID"`
	ObservedAt        time.Time            `json:"observedAt"`
	Provider          *ProviderObservation `json:"provider,omitempty"`
	GatewayRulesReady bool                 `json:"gatewayRulesReady"`
	Delivered         bool                 `json:"delivered"`
	Acknowledged      bool                 `json:"acknowledged"`
	Withdrawn         bool                 `json:"withdrawn"`
}

type ProviderObservation struct {
	PublicAddress netip.Addr `json:"publicAddress"`
	PublicPort    uint16     `json:"publicPort"`
	ExpiresAt     time.Time  `json:"expiresAt"`
}

type WithdrawalIntent struct {
	APIVersion                 string                    `json:"apiVersion"`
	LeaseNamespace             wayv1.NamespaceName       `json:"leaseNamespace"`
	LeaseUID                   wayv1.ObjectUID           `json:"leaseUID"`
	GatewayUID                 wayv1.ObjectUID           `json:"gatewayUID"`
	HandoffGeneration          int64                     `json:"handoffGeneration"`
	ServiceUID                 wayv1.ObjectUID           `json:"serviceUID"`
	EndpointSliceUID           wayv1.ObjectUID           `json:"endpointSliceUID"`
	PodUID                     wayv1.ObjectUID           `json:"podUID"`
	ApplicationEndpointRetired bool                      `json:"applicationEndpointRetired,omitempty"`
	ReleaseProvider            bool                      `json:"releaseProvider,omitempty"`
	ProviderInternalPort       uint16                    `json:"providerInternalPort,omitempty"`
	PreviousPublicPort         uint16                    `json:"previousPublicPort,omitempty"`
	Protocols                  []wayv1.TransportProtocol `json:"protocols,omitempty"`
	AdapterName                wayv1.ObjectName          `json:"adapterName,omitempty"`
}

// Runtime is implemented by the narrow authenticated gateway-runtime client.
// The runtime owns provider acquisition, atomic gateway DNAT/SNAT, neutral
// delivery and adapter acknowledgement observation. It receives no Kubernetes
// credential and cannot write status.
type Runtime interface {
	Reconcile(context.Context, *wayv1.VPNGateway, Intent) (Observation, error)
	Withdraw(context.Context, *wayv1.VPNGateway, WithdrawalIntent) (Observation, error)
	Quarantine(context.Context, *wayv1.VPNGateway, WithdrawalIntent, time.Time) error
}

func WithdrawalFor(lease *wayv1.PortForwardLease, gateway *wayv1.VPNGateway) WithdrawalIntent {
	intent := WithdrawalIntent{APIVersion: RuntimeAPIVersion, LeaseNamespace: wayv1.NamespaceName(lease.Namespace), LeaseUID: wayv1.ObjectUID(lease.UID), GatewayUID: wayv1.ObjectUID(gateway.UID), HandoffGeneration: lease.Status.HandoffGeneration}
	if lease.Status.ActiveEndpoint != nil {
		intent.ServiceUID = lease.Status.ActiveEndpoint.ServiceUID
		intent.EndpointSliceUID = lease.Status.ActiveEndpoint.EndpointSliceUID
		intent.PodUID = lease.Status.ActiveEndpoint.PodUID
	}
	intent.Protocols = append([]wayv1.TransportProtocol(nil), lease.Spec.Protocols...)
	if lease.Spec.ApplicationAdapterRef != nil {
		intent.AdapterName = lease.Spec.ApplicationAdapterRef.Name
	}
	if lease.Status.Provider != nil && lease.Status.Provider.PublicPort > 0 && lease.Status.Provider.PublicPort <= 65535 {
		intent.PreviousPublicPort = uint16(lease.Status.Provider.PublicPort)
	}
	return intent
}

func IntentFor(lease *wayv1.PortForwardLease, gateway *wayv1.VPNGateway, candidate Candidate, generation int64) Intent {
	intent := Intent{
		APIVersion: RuntimeAPIVersion, LeaseNamespace: wayv1.NamespaceName(lease.Namespace), LeaseUID: wayv1.ObjectUID(lease.UID), GatewayUID: wayv1.ObjectUID(gateway.UID),
		HandoffGeneration: generation, ServiceUID: candidate.ServiceUID, EndpointSliceUID: candidate.EndpointSliceUID,
		PodUID: candidate.PodUID, BindingUID: candidate.BindingUID, OverlayAddress: candidate.OverlayAddress, ApplicationAddress: candidate.ApplicationAddress,
		BackendPort: candidate.TargetPort, TargetPort: candidate.TargetPort, Protocols: append([]wayv1.TransportProtocol(nil), lease.Spec.Protocols...),
		ApplicationPortMode: ApplicationPortFixed,
	}
	if lease.Spec.ApplicationAdapterRef != nil {
		intent.AdapterName = lease.Spec.ApplicationAdapterRef.Name
	}
	if lease.Status.Provider != nil && lease.Status.Provider.PublicPort > 0 && lease.Status.Provider.PublicPort <= 65535 {
		intent.PreviousPublicPort = uint16(lease.Status.Provider.PublicPort)
	}
	return intent
}

func ExactObservation(observation Observation, intent Intent) bool {
	return observation.APIVersion == RuntimeAPIVersion && observation.LeaseUID == intent.LeaseUID &&
		observation.GatewayUID == intent.GatewayUID && observation.HandoffGeneration == intent.HandoffGeneration &&
		observation.PodUID == intent.PodUID && !observation.ObservedAt.IsZero()
}

func ExactWithdrawal(observation Observation, intent WithdrawalIntent) bool {
	return observation.APIVersion == RuntimeAPIVersion && observation.LeaseUID == intent.LeaseUID &&
		observation.GatewayUID == intent.GatewayUID && observation.HandoffGeneration == intent.HandoffGeneration &&
		observation.PodUID == intent.PodUID && !observation.ObservedAt.IsZero() && observation.Withdrawn
}
