// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	waycni "github.com/Amoenus/waycloak/internal/cni"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDecodeRequestRejectsUnknownAndTrailingInput(t *testing.T) {
	validPod := `"pod":{"namespace":"apps","name":"client","uid":"pod-uid","containerID":"sandbox","ifName":"eth0","netNS":"/var/run/netns/client"}`
	for name, body := range map[string]string{
		"unknown field":  `{"apiVersion":"` + waycni.AgentAPIVersion + `",` + validPod + `,"unknown":true}`,
		"trailing value": `{"apiVersion":"` + waycni.AgentAPIVersion + `",` + validPod + `}{}`,
		"wrong version":  `{"apiVersion":"networking.waycloak.io/cni-node/v2",` + validPod + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/cni-node/v1/resolve", bytes.NewBufferString(body))
			if _, err := decodeRequest(httptest.NewRecorder(), request); err == nil {
				t.Fatal("hostile request was decoded")
			}
		})
	}
}

func TestHandlerRejectsPodNameReuseWithStableIdentityError(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "apps",
		Name:      "client",
		UID:       types.UID("current-uid"),
	}})
	request := httptest.NewRequest(http.MethodPost, "/cni-node/v1/resolve", bytes.NewBufferString(
		`{"apiVersion":"`+waycni.AgentAPIVersion+`","pod":{"namespace":"apps","name":"client","uid":"replaced-uid","containerID":"sandbox","ifName":"eth0","netNS":"/var/run/netns/client"}}`,
	))
	response := httptest.NewRecorder()
	handler(client).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	var output waycni.AgentResponse
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		t.Fatal(err)
	}
	if output.Error == nil || output.Error.Code != waycni.AgentErrorPodIdentityMismatch || output.Error.Retryable {
		t.Fatalf("identity error = %#v", output.Error)
	}
}
