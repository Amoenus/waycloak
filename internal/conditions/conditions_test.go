// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package conditions

import (
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildUsesCurrentGenerationAndStableTransitions(t *testing.T) {
	t.Parallel()
	oldTime := metav1.NewTime(time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC))
	now := oldTime.Add(24 * time.Hour)
	previous := []metav1.Condition{
		{Type: wayv1.ConditionAccepted, Status: metav1.ConditionTrue, Reason: wayv1.ReasonAccepted, ObservedGeneration: 1, LastTransitionTime: oldTime},
		{Type: wayv1.ConditionReady, Status: metav1.ConditionFalse, Reason: wayv1.ReasonNotReady, ObservedGeneration: 1, LastTransitionTime: oldTime},
	}
	states := map[string]State{
		wayv1.ConditionAccepted:     True(wayv1.ReasonAccepted, "Intent is accepted"),
		wayv1.ConditionResolvedRefs: True(wayv1.ReasonResolvedRefs, "References are resolved"),
		wayv1.ConditionProgrammed:   False(wayv1.ReasonPending, "Programming is pending"),
		wayv1.ConditionReady:        Unknown("Live observation is unavailable"),
	}
	order := SummaryOrder()
	got := Build(previous, 2, now, order, states)
	if len(got) != len(order) {
		t.Fatalf("condition count = %d, want %d", len(got), len(order))
	}
	for i := range got {
		if got[i].Type != order[i] || got[i].ObservedGeneration != 2 {
			t.Fatalf("condition[%d] = %#v", i, got[i])
		}
	}
	if !got[0].LastTransitionTime.Equal(&oldTime) {
		t.Fatalf("unchanged Accepted transition = %s, want %s", got[0].LastTransitionTime, oldTime)
	}
	wantNow := metav1.NewTime(now)
	if !got[3].LastTransitionTime.Equal(&wantNow) {
		t.Fatalf("changed Ready transition = %s, want %s", got[3].LastTransitionTime, now)
	}
	if got[3].Status != metav1.ConditionUnknown || got[3].Reason != wayv1.ReasonObservationUnavailable {
		t.Fatalf("unknown condition = %#v", got[3])
	}
}

func TestBuildKeepsMissingSummaryObservationUnknown(t *testing.T) {
	t.Parallel()
	got := Build(nil, 5, time.Now(), SummaryOrder(), map[string]State{
		wayv1.ConditionAccepted: True(wayv1.ReasonAccepted, "Intent is accepted"),
	})
	if len(got) != 4 {
		t.Fatalf("condition count = %d, want 4", len(got))
	}
	for _, condition := range got[1:] {
		if condition.Status != metav1.ConditionUnknown || condition.Reason != wayv1.ReasonObservationUnavailable || condition.ObservedGeneration != 5 {
			t.Fatalf("missing observation condition = %#v", condition)
		}
	}
}

func TestCurrentRejectsStaleStatusAndConditionGenerations(t *testing.T) {
	t.Parallel()
	values := []metav1.Condition{{Type: wayv1.ConditionReady, Status: metav1.ConditionTrue, Reason: wayv1.ReasonReady, ObservedGeneration: 3}}
	if CurrentTrue(values, wayv1.ConditionReady, 2, 3) {
		t.Fatal("stale enclosing status reported current Ready")
	}
	if CurrentTrue(values, wayv1.ConditionReady, 3, 4) {
		t.Fatal("stale condition reported current Ready")
	}
	if !CurrentTrue(values, wayv1.ConditionReady, 3, 3) {
		t.Fatal("current Ready was not observed")
	}
}
