// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrent

import (
	"errors"
	"time"
)

const (
	DefaultTransientFailureLimit  = 3
	DefaultTransientFailureWindow = 15 * time.Second
)

type ReadinessPhase string

const (
	ReadinessPending  ReadinessPhase = "Pending"
	ReadinessDegraded ReadinessPhase = "Degraded"
	ReadinessReady    ReadinessPhase = "Ready"
)

type ReadinessDecision struct {
	Ready               bool
	Phase               ReadinessPhase
	Changed             bool
	ConsecutiveFailures int
}

type ReadinessState struct {
	TransientFailureLimit  int
	TransientFailureWindow time.Duration

	phase               ReadinessPhase
	ready               bool
	applied             LeaseRevision
	consecutiveFailures int
	firstFailure        time.Time
}

func (state *ReadinessState) Observe(revision LeaseRevision, reconcileErr error, now time.Time) ReadinessDecision {
	previousPhase := state.phase
	if previousPhase == "" {
		previousPhase = ReadinessPending
		state.phase = ReadinessPending
	}

	if reconcileErr == nil {
		state.phase = ReadinessReady
		state.ready = true
		state.applied = revision
		state.resetFailures()
		return state.decision(previousPhase)
	}

	var typed *ReconcileError
	transient := errors.As(reconcileErr, &typed) && typed.Kind == FailureTransientControlObservation
	if transient && state.ready && revision == state.applied {
		if state.consecutiveFailures == 0 {
			state.firstFailure = now
		}
		state.consecutiveFailures++
		if state.consecutiveFailures < state.failureLimit() && now.Sub(state.firstFailure) < state.failureWindow() {
			state.phase = ReadinessDegraded
			return state.decision(previousPhase)
		}
	}

	state.phase = ReadinessPending
	state.ready = false
	if !transient {
		state.resetFailures()
	}
	return state.decision(previousPhase)
}

func (state *ReadinessState) decision(previousPhase ReadinessPhase) ReadinessDecision {
	return ReadinessDecision{Ready: state.ready, Phase: state.phase, Changed: state.phase != previousPhase, ConsecutiveFailures: state.consecutiveFailures}
}

func (state *ReadinessState) resetFailures() {
	state.consecutiveFailures = 0
	state.firstFailure = time.Time{}
}

func (state *ReadinessState) failureLimit() int {
	if state.TransientFailureLimit > 0 {
		return state.TransientFailureLimit
	}
	return DefaultTransientFailureLimit
}

func (state *ReadinessState) failureWindow() time.Duration {
	if state.TransientFailureWindow > 0 {
		return state.TransientFailureWindow
	}
	return DefaultTransientFailureWindow
}
