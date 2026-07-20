// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package dataplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func TestHTTPWorkloadObserver(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workload/observation" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		doc := WorkloadObservationDocument{
			APIVersion: WorkloadObservationAPIVersion,
			Observation: WorkloadObservation{
				PodUID:               types.UID("test-uid"),
				AllocationGeneration: 2,
				GatewayGeneration:    5,
			},
		}
		json.NewEncoder(w).Encode(doc)
	}))
	defer ts.Close()

	observer := HTTPWorkloadObserver{Client: ts.Client()}

	// Strip "http://" from URL since endpoint expects host:port
	endpoint := ts.URL[7:]

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	obs, err := observer.Observe(ctx, endpoint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.PodUID != "test-uid" {
		t.Errorf("expected podUID test-uid, got %s", obs.PodUID)
	}
	if obs.AllocationGeneration != 2 {
		t.Errorf("expected allocationGeneration 2, got %d", obs.AllocationGeneration)
	}
	if obs.GatewayGeneration != 5 {
		t.Errorf("expected gatewayGeneration 5, got %d", obs.GatewayGeneration)
	}
}
