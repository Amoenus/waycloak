// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const ManagerObservationAPIVersion = "networking.waycloak.io/v1alpha1"

type ManagerObservation struct {
	AppliedMembershipGeneration string `json:"appliedMembershipGeneration,omitempty"`
}

type ManagerObservationDocument struct {
	APIVersion  string             `json:"apiVersion"`
	Observation ManagerObservation `json:"observation"`
}

type ManagerObserver interface {
	Observe(context.Context, string) (ManagerObservation, error)
}

type HTTPManagerObserver struct{ Client *http.Client }

func (observer *HTTPManagerObserver) Observe(ctx context.Context, endpoint string) (ManagerObservation, error) {
	client := observer.Client
	if client == nil {
		client = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/v1/gateway/observation", nil)
	if err != nil {
		return ManagerObservation{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return ManagerObservation{}, fmt.Errorf("observe gateway manager: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return ManagerObservation{}, fmt.Errorf("observe gateway manager: status %d", response.StatusCode)
	}
	var document ManagerObservationDocument
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		return ManagerObservation{}, fmt.Errorf("decode gateway manager observation: %w", err)
	}
	if document.APIVersion != ManagerObservationAPIVersion {
		return ManagerObservation{}, fmt.Errorf("unsupported gateway manager observation API %q", document.APIVersion)
	}
	if document.Observation.AppliedMembershipGeneration != "" && !membershipGenerationPattern.MatchString(document.Observation.AppliedMembershipGeneration) {
		return ManagerObservation{}, fmt.Errorf("gateway manager returned an invalid membership generation")
	}
	return document.Observation, nil
}
