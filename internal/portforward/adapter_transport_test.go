// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
)

func TestHTTPAdapterClientUsesDeterministicCredentialFreeExactEndpoint(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	intent := managerIntent("pod-a", 4)
	intent.AdapterName = "qbittorrent"
	record := AdapterLeaseRecord{APIVersion: AdapterAPIVersion, LeaseNamespace: intent.LeaseNamespace, LeaseUID: intent.LeaseUID,
		HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID, ExpiresAt: now.Add(time.Minute)}
	requests := 0
	client := &HTTPAdapterClient{Port: 9443, ClusterDomain: "cluster.local", Now: func() time.Time { return time.Now().UTC() }, Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Scheme != "https" || request.URL.Host != AdapterServiceName(intent.LeaseNamespace, intent.AdapterName)+".apps.svc.cluster.local:9443" ||
			request.URL.Path != adapterPath(intent.LeaseUID, "") || request.Header.Get("Authorization") != "" {
			t.Fatalf("adapter request = %s headers=%v", request.URL, request.Header)
		}
		var got AdapterLeaseRecord
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil || got.LeaseUID != record.LeaseUID || got.PodUID != record.PodUID {
			t.Fatalf("adapter record = %#v, %v", got, err)
		}
		ack := AdapterAcknowledgement{APIVersion: AdapterAPIVersion, LeaseNamespace: record.LeaseNamespace, LeaseUID: record.LeaseUID,
			HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID, ObservedAt: now, ExpiresAt: record.ExpiresAt}
		return adapterJSONResponse(t, ack), nil
	})}}
	acknowledgement, err := client.Deliver(context.Background(), intent.AdapterName, record)
	if err != nil || acknowledgement.PodUID != intent.PodUID || requests != 1 {
		t.Fatalf("adapter acknowledgement = %#v, calls=%d, %v", acknowledgement, requests, err)
	}
}

func TestHTTPAdapterClientRejectsMismatchedWithdrawalAcknowledgement(t *testing.T) {
	intent := WithdrawalIntent{APIVersion: RuntimeAPIVersion, LeaseNamespace: "apps", LeaseUID: "lease-uid", GatewayUID: "gateway-uid", HandoffGeneration: 2, PodUID: "pod-a", AdapterName: "qbittorrent"}
	client := &HTTPAdapterClient{Port: 9443, ClusterDomain: "cluster.local", Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != adapterPath(intent.LeaseUID, "withdraw") {
			t.Fatalf("withdrawal path = %s", request.URL.Path)
		}
		return adapterJSONResponse(t, AdapterWithdrawalAcknowledgement{APIVersion: AdapterAPIVersion, LeaseNamespace: intent.LeaseNamespace,
			LeaseUID: intent.LeaseUID, HandoffGeneration: intent.HandoffGeneration, PodUID: "different-pod", ObservedAt: time.Now(), Withdrawn: true}), nil
	})}}
	if withdrawn, err := client.Withdraw(context.Background(), intent.AdapterName, intent); err != nil || withdrawn {
		t.Fatalf("mismatched withdrawal = %t, %v", withdrawn, err)
	}
}

func TestHTTPAdapterClientObservesExactHealthyAdapterIdentity(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	namespace := wayv1.NamespaceName("apps")
	name := wayv1.ObjectName("qbittorrent")
	image := "registry.invalid/adapter@sha256:" + strings.Repeat("a", 64)
	client := &HTTPAdapterClient{Port: DefaultAdapterPort, ClusterDomain: "cluster.local", Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/networking.waycloak.io/adapter/v1/healthz" ||
			request.URL.Host != AdapterServiceName(namespace, name)+".apps.svc.cluster.local:9444" || request.Header.Get("Authorization") != "" {
			t.Fatalf("adapter health request = %s %s headers=%v", request.Method, request.URL, request.Header)
		}
		return adapterJSONResponse(t, AdapterHealthObservation{APIVersion: AdapterAPIVersion, Namespace: namespace, Name: name,
			Image: image, PodUID: "adapter-pod-uid", ObservedAt: now, Ready: true}), nil
	})}}
	observation, err := client.Observe(context.Background(), namespace, name, image)
	if err != nil || !observation.Ready || observation.PodUID != "adapter-pod-uid" {
		t.Fatalf("adapter health observation = %#v, %v", observation, err)
	}

	client.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return adapterJSONResponse(t, AdapterHealthObservation{APIVersion: AdapterAPIVersion, Namespace: namespace, Name: "other",
			Image: image, PodUID: "adapter-pod-uid", ObservedAt: now, Ready: true}), nil
	})
	if _, err := client.Observe(context.Background(), namespace, name, image); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("mismatched adapter health error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func adapterJSONResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
