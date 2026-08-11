// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"testing"
	"time"

	waybinding "github.com/Amoenus/waycloak/internal/binding"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestWaitBindingReadyRequiresCurrentObservedGeneration(t *testing.T) {
	ctx := context.Background()
	clients := supportedClients(t)
	podUID := types.UID("pod-uid")
	gvr := schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpnworkloadbindings"}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.waycloak.io/v1beta1",
		"kind":       "VPNWorkloadBinding",
		"metadata": map[string]any{
			"name":       waybinding.BindingName(podUID),
			"namespace":  "apps",
			"generation": int64(2),
		},
		"status": map[string]any{"conditions": []any{map[string]any{
			"type":               "Ready",
			"status":             "True",
			"observedGeneration": int64(1),
		}}},
	}}
	created, err := clients.Dynamic.Resource(gvr).Namespace("apps").Create(ctx, binding, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitBindingReady(ctx, clients, "apps", podUID, 5*time.Millisecond); err == nil {
		t.Fatal("stale Ready condition was accepted")
	}
	if err := unstructured.SetNestedSlice(created.Object, []any{map[string]any{
		"type":               "Ready",
		"status":             "True",
		"observedGeneration": int64(2),
	}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Dynamic.Resource(gvr).Namespace("apps").Update(ctx, created, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := waitBindingReady(ctx, clients, "apps", podUID, time.Second); err != nil {
		t.Fatal(err)
	}
}
