// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrent

import (
	"errors"
	"testing"
	"time"
)

func TestReadinessRetainsExactAppliedGenerationAcrossBriefControlAPIStall(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	revision := LeaseRevision{Identity: "lease-uid", Generation: 17, ApplicationPort: 64327}
	state := &ReadinessState{}
	decision := state.Observe(revision, nil, now)
	if !decision.Ready || decision.Phase != ReadinessReady {
		t.Fatalf("initial decision = %#v", decision)
	}
	stall := &ReconcileError{Kind: FailureTransientControlObservation, Revision: revision, Err: errors.New("qBitTorrent API timeout")}
	decision = state.Observe(revision, stall, now.Add(2*time.Second))
	if !decision.Ready || decision.Phase != ReadinessDegraded || !decision.Changed {
		t.Fatalf("first transient decision = %#v", decision)
	}
	decision = state.Observe(revision, stall, now.Add(4*time.Second))
	if !decision.Ready || decision.Phase != ReadinessDegraded || decision.Changed {
		t.Fatalf("second transient decision = %#v", decision)
	}
	decision = state.Observe(revision, nil, now.Add(5*time.Second))
	if !decision.Ready || decision.Phase != ReadinessReady || !decision.Changed {
		t.Fatalf("recovery decision = %#v", decision)
	}
}

func TestReadinessWithdrawsAfterBoundedConsecutiveControlAPIFailures(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	revision := LeaseRevision{Identity: "lease-uid", Generation: 17, ApplicationPort: 64327}
	state := &ReadinessState{}
	state.Observe(revision, nil, now)
	stall := &ReconcileError{Kind: FailureTransientControlObservation, Revision: revision, Err: errors.New("qBitTorrent API timeout")}
	state.Observe(revision, stall, now.Add(2*time.Second))
	state.Observe(revision, stall, now.Add(4*time.Second))
	decision := state.Observe(revision, stall, now.Add(6*time.Second))
	if decision.Ready || decision.Phase != ReadinessPending || !decision.Changed {
		t.Fatalf("sustained failure decision = %#v", decision)
	}
}

func TestReadinessWithdrawsImmediatelyForRotationOrCriticalFailure(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	applied := LeaseRevision{Identity: "lease-uid", Generation: 17, ApplicationPort: 64327}
	rotated := LeaseRevision{Identity: "lease-uid", Generation: 18, ApplicationPort: 61000}
	state := &ReadinessState{}
	state.Observe(applied, nil, now)
	stall := &ReconcileError{Kind: FailureTransientControlObservation, Revision: rotated, Err: errors.New("qBitTorrent API timeout")}
	if decision := state.Observe(rotated, stall, now.Add(time.Second)); decision.Ready || decision.Phase != ReadinessPending {
		t.Fatalf("rotation failure decision = %#v", decision)
	}

	state.Observe(applied, nil, now.Add(2*time.Second))
	listenerLoss := &ReconcileError{Kind: FailureCritical, Revision: applied, Err: errors.New("listener unavailable")}
	if decision := state.Observe(applied, listenerLoss, now.Add(3*time.Second)); decision.Ready || decision.Phase != ReadinessPending {
		t.Fatalf("listener loss decision = %#v", decision)
	}
}

func TestReadinessWindowBoundsSparseTransientFailures(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	revision := LeaseRevision{Identity: "lease-uid", Generation: 17, ApplicationPort: 64327}
	state := &ReadinessState{TransientFailureLimit: 10, TransientFailureWindow: 5 * time.Second}
	state.Observe(revision, nil, now)
	stall := &ReconcileError{Kind: FailureTransientControlObservation, Revision: revision, Err: errors.New("qBitTorrent API timeout")}
	state.Observe(revision, stall, now.Add(time.Second))
	decision := state.Observe(revision, stall, now.Add(6*time.Second))
	if decision.Ready || decision.Phase != ReadinessPending {
		t.Fatalf("window-bounded decision = %#v", decision)
	}
}
