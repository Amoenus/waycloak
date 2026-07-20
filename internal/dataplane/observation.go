// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package dataplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

const WorkloadObservationAPIVersion = "networking.waycloak.io/v1alpha1"

type WorkloadObservation struct {
	PodUID               types.UID `json:"podUID"`
	AllocationGeneration int64     `json:"allocationGeneration"`
	GatewayGeneration    int64     `json:"gatewayGeneration"`
}

type WorkloadObservationDocument struct {
	APIVersion  string              `json:"apiVersion"`
	Observation WorkloadObservation `json:"observation"`
}

type WorkloadObserver interface {
	Observe(context.Context, string) (WorkloadObservation, error)
}

type HTTPWorkloadObserver struct{ Client *http.Client }

func (observer *HTTPWorkloadObserver) Observe(ctx context.Context, endpoint string) (WorkloadObservation, error) {
	client := observer.Client
	if client == nil {
		client = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/v1/workload/observation", nil)
	if err != nil {
		return WorkloadObservation{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return WorkloadObservation{}, fmt.Errorf("observe workload agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return WorkloadObservation{}, fmt.Errorf("observe workload agent: status %d", response.StatusCode)
	}
	var document WorkloadObservationDocument
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		return WorkloadObservation{}, fmt.Errorf("decode workload agent observation: %w", err)
	}
	if document.APIVersion != WorkloadObservationAPIVersion {
		return WorkloadObservation{}, fmt.Errorf("unsupported workload agent observation API %q", document.APIVersion)
	}
	return document.Observation, nil
}
