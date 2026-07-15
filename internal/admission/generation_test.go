// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package admission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Amoenus/waycloak/internal/contract"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestGenerationGateTracksDesiredConfigMapWithoutCaching(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "admission", Namespace: "system"}, Data: map[string]string{contract.AdmissionGenerationKey: "old"}}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build()
	oldGate := &GenerationGate{Reader: reader, Namespace: "system", ConfigMap: "admission", Generation: "old"}
	newGate := &GenerationGate{Reader: reader, Namespace: "system", ConfigMap: "admission", Generation: "new"}
	if err := oldGate.Check(context.Background()); err != nil {
		t.Fatalf("old gate before transition: %v", err)
	}
	if err := newGate.Check(context.Background()); err == nil {
		t.Fatal("new gate became ready before the desired generation changed")
	}
	var current corev1.ConfigMap
	if err := reader.Get(context.Background(), types.NamespacedName{Namespace: "system", Name: "admission"}, &current); err != nil {
		t.Fatal(err)
	}
	current.Data[contract.AdmissionGenerationKey] = "new"
	if err := reader.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	if err := oldGate.Check(context.Background()); err == nil {
		t.Fatal("old gate remained ready after the desired generation changed")
	}
	if err := newGate.Check(context.Background()); err != nil {
		t.Fatalf("new gate after transition: %v", err)
	}
}

func TestValidatorGenerationGateLeavesUnannotatedPodsUnaffected(t *testing.T) {
	mutator := testMutator(t, metav1.LabelSelector{})
	mutator.GenerationGate.Generation = "stale"
	validator := &PodValidator{AgentImage: testAgentImage, GenerationGate: mutator.GenerationGate}
	plain := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "apps"}}
	raw, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	response := validator.Handle(context.Background(), cradmission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Object: runtime.RawExtension{Raw: raw}}})
	if !response.Allowed {
		t.Fatalf("stale validator blocked an unannotated Pod: %#v", response.Result)
	}
	protected := plain.DeepCopy()
	protected.Annotations = map[string]string{contract.GatewayAnnotation: "egress/private"}
	raw, err = json.Marshal(protected)
	if err != nil {
		t.Fatal(err)
	}
	response = validator.Handle(context.Background(), cradmission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Object: runtime.RawExtension{Raw: raw}}})
	if response.Allowed || response.Result == nil || !strings.Contains(response.Result.Message, waystatus.ReasonAdmissionGenerationConflict) {
		t.Fatalf("stale validator response = %#v", response)
	}
}
