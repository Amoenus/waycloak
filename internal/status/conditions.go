// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package status

import (
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ConditionAccepted                     = "Accepted"
	ConditionAllocated                    = "Allocated"
	ConditionAllocationPublished          = "AllocationPublished"
	ConditionReady                        = "Ready"
	ReasonAccepted                        = "Accepted"
	ReasonInvalidOverlay                  = "InvalidOverlay"
	ReasonAllocationPending               = "AllocationPending"
	ReasonAllocationPersisted             = "AllocationPersisted"
	ReasonAllocationConfigMapReady        = "AllocationConfigMapReady"
	ReasonDataPlaneNotImplemented         = "DataPlaneNotImplemented"
	ReasonGatewayNotFound                 = "GatewayNotFound"
	ReasonUnauthorizedGateway             = "UnauthorizedGateway"
	ReasonAdmissionVersionConflict        = "AdmissionVersionConflict"
	ReasonApplicationCredentialsForbidden = "ApplicationCredentialsForbidden"
)

func Set(conditions *[]metav1.Condition, generation int64, typ string, value metav1.ConditionStatus, reason, message string) {
	apiMeta.SetStatusCondition(conditions, metav1.Condition{Type: typ, Status: value, ObservedGeneration: generation, Reason: reason, Message: message})
}
