// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package conditions implements the replacement API's positive-polarity
// condition contract. It deliberately contains no resource-specific readiness
// inference: callers must supply observations from the responsible component.
package conditions

import (
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type State struct {
	Status  metav1.ConditionStatus
	Reason  string
	Message string
}

func True(reason, message string) State {
	return State{Status: metav1.ConditionTrue, Reason: reason, Message: message}
}

func False(reason, message string) State {
	return State{Status: metav1.ConditionFalse, Reason: reason, Message: message}
}

func Unknown(message string) State {
	return State{Status: metav1.ConditionUnknown, Reason: wayv1.ReasonObservationUnavailable, Message: message}
}

// Build creates conditions in the declared stable order. A transition time is
// retained when polarity is unchanged, even if the reason, message, or
// observed generation advances.
func Build(previous []metav1.Condition, generation int64, now time.Time, order []string, states map[string]State) []metav1.Condition {
	result := make([]metav1.Condition, 0, len(order))
	for _, conditionType := range order {
		state, ok := states[conditionType]
		if !ok {
			state = Unknown("Condition observation is unavailable")
		}
		transition := metav1.NewTime(now.UTC())
		if old := apiMeta.FindStatusCondition(previous, conditionType); old != nil && old.Status == state.Status {
			transition = old.LastTransitionTime
		}
		result = append(result, metav1.Condition{
			Type:               conditionType,
			Status:             state.Status,
			Reason:             state.Reason,
			Message:            state.Message,
			ObservedGeneration: generation,
			LastTransitionTime: transition,
		})
	}
	return result
}

// Current returns a condition only when both the condition and enclosing
// status observation are for the resource's current generation.
func Current(values []metav1.Condition, conditionType string, statusGeneration, generation int64) *metav1.Condition {
	if statusGeneration != generation {
		return nil
	}
	condition := apiMeta.FindStatusCondition(values, conditionType)
	if condition == nil || condition.ObservedGeneration != generation {
		return nil
	}
	return condition
}

func CurrentTrue(values []metav1.Condition, conditionType string, statusGeneration, generation int64) bool {
	condition := Current(values, conditionType, statusGeneration, generation)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

func SummaryOrder() []string {
	return []string{
		wayv1.ConditionAccepted,
		wayv1.ConditionResolvedRefs,
		wayv1.ConditionProgrammed,
		wayv1.ConditionReady,
	}
}
