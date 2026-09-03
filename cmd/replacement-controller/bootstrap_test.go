// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestObservationCertificateBootstrapIsIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	bootstrapID := "sha256:" + strings.Repeat("a", 64)
	arguments := []string{"--namespace", "waycloak-system", "--release", "waycloak", "--bootstrap-id", bootstrapID}

	for range 2 {
		if err := ensureObservationCertificateBootstrap(context.Background(), client, arguments); err != nil {
			t.Fatalf("bootstrap observation certificates: %v", err)
		}
	}

	secrets, err := client.CoreV1().Secrets("waycloak-system").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	if len(secrets.Items) != 2 {
		t.Fatalf("got %d observation secrets, want 2", len(secrets.Items))
	}
	for _, secret := range secrets.Items {
		if secret.Annotations["install.waycloak.io/release"] != "waycloak" || secret.Annotations["install.waycloak.io/initial-plan-id"] != bootstrapID {
			t.Fatalf("secret %s lacks exact bootstrap ownership: %#v", secret.Name, secret.Annotations)
		}
	}
}

func TestWaitForControllerReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			http.NotFound(response, request)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := waitForControllerReady([]string{"--url", server.URL + "/readyz", "--timeout", "2s"}); err != nil {
		t.Fatalf("wait for ready controller: %v", err)
	}
}

func TestObservationCertificateBootstrapRejectsIncompleteIntent(t *testing.T) {
	client := fake.NewSimpleClientset()
	if err := ensureObservationCertificateBootstrap(context.Background(), client, []string{"--namespace", "waycloak-system"}); err == nil {
		t.Fatal("incomplete bootstrap intent unexpectedly succeeded")
	}
}
