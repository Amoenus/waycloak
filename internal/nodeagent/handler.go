// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	waycni "github.com/Amoenus/waycloak/internal/cni"
)

func Handler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cni-node/v1/status", func(response http.ResponseWriter, _ *http.Request) {
		if service == nil || service.Reader == nil || service.Programmer == nil || service.NodeName == "" || !service.Ready() {
			writeFailure(response, http.StatusServiceUnavailable, "AgentUnavailable", true, "Node agent is not ready")
			return
		}
		status := service.Status()
		writeJSON(response, http.StatusOK, waycni.AgentResponse{APIVersion: waycni.AgentAPIVersion, Status: &status})
	})
	operation := func(kind string) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			var input waycni.AgentRequest
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil || input.APIVersion != waycni.AgentAPIVersion {
				writeFailure(response, http.StatusBadRequest, waycni.AgentErrorInvalidRequest, false, "Local request is invalid")
				return
			}
			var trailing struct{}
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				writeFailure(response, http.StatusBadRequest, waycni.AgentErrorInvalidRequest, false, "Local request is invalid")
				return
			}
			switch kind {
			case "resolve":
				resolution, err := service.Resolve(request.Context(), input.Pod)
				if err != nil {
					writeServiceError(response, err)
					return
				}
				writeJSON(response, http.StatusOK, waycni.AgentResponse{APIVersion: waycni.AgentAPIVersion, Resolution: &resolution})
			case "binding":
				binding, err := service.Binding(request.Context(), input.Pod)
				if err != nil {
					writeServiceError(response, err)
					return
				}
				writeJSON(response, http.StatusOK, waycni.AgentResponse{APIVersion: waycni.AgentAPIVersion, Binding: &binding})
			case "prepare":
				if input.Binding == nil {
					writeFailure(response, http.StatusBadRequest, waycni.AgentErrorInvalidRequest, false, "Binding identity is required")
					return
				}
				if err := service.Prepare(request.Context(), input.Pod, *input.Binding); err != nil {
					writeServiceError(response, err)
					return
				}
				response.WriteHeader(http.StatusNoContent)
			case "check":
				if input.Binding == nil {
					writeFailure(response, http.StatusBadRequest, waycni.AgentErrorInvalidRequest, false, "Binding identity is required")
					return
				}
				if err := service.Check(request.Context(), input.Pod, *input.Binding); err != nil {
					writeServiceError(response, err)
					return
				}
				response.WriteHeader(http.StatusNoContent)
			case "withdraw":
				if err := service.Withdraw(request.Context(), input.Pod); err != nil {
					writeServiceError(response, err)
					return
				}
				response.WriteHeader(http.StatusNoContent)
			}
		}
	}
	mux.HandleFunc("POST /cni-node/v1/resolve", operation("resolve"))
	mux.HandleFunc("POST /cni-node/v1/binding", operation("binding"))
	mux.HandleFunc("POST /cni-node/v1/prepare", operation("prepare"))
	mux.HandleFunc("POST /cni-node/v1/check", operation("check"))
	mux.HandleFunc("POST /cni-node/v1/withdraw", operation("withdraw"))
	return mux
}

func writeServiceError(response http.ResponseWriter, err error) {
	if errors.Is(err, waycni.ErrBindingNotReady) {
		writeFailure(response, http.StatusConflict, waycni.AgentErrorBindingNotReady, true, "Binding is not ready")
		return
	}
	for authorityError, message := range map[error]string{
		ErrPodIdentityInvalid: "Pod identity is invalid",
		ErrPodLookupFailed:    "Kubernetes Pod observation failed",
		ErrPodUIDMismatch:     "Pod UID does not match API observation",
		ErrPodNodeMismatch:    "Pod node does not match local authority",
	} {
		if errors.Is(err, authorityError) {
			writeFailure(response, http.StatusForbidden, waycni.AgentErrorPodIdentityMismatch, false, message)
			return
		}
	}
	writeFailure(response, http.StatusForbidden, waycni.AgentErrorPodIdentityMismatch, false, "Exact local authority check failed")
}

func writeFailure(response http.ResponseWriter, status int, code string, retryable bool, message string) {
	writeJSON(response, status, waycni.AgentResponse{APIVersion: waycni.AgentAPIVersion, Error: &waycni.AgentError{Code: code, Retryable: retryable, Message: message}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(response, "encode local response", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = response.Write(data)
}
