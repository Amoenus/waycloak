// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"testing"
	"time"
)

func TestPublicationTransitionsReportFailureSequenceAndRecovery(t *testing.T) {
	started := time.Unix(100, 0)
	transitions := &publicationTransitions{}
	if got := transitions.failed(started); got != 1 {
		t.Fatalf("first failure count = %d", got)
	}
	if got := transitions.failed(started.Add(time.Second)); got != 2 {
		t.Fatalf("second failure count = %d", got)
	}
	failures, duration := transitions.recovered(started.Add(3 * time.Second))
	if failures != 2 || duration != 3*time.Second {
		t.Fatalf("recovery = failures %d duration %s", failures, duration)
	}
	if failures, duration = transitions.recovered(started.Add(4 * time.Second)); failures != 0 || duration != 0 {
		t.Fatalf("steady success emitted recovery = failures %d duration %s", failures, duration)
	}
}
