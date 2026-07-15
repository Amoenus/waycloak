// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package admission

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Amoenus/waycloak/internal/contract"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GenerationGate compares this webhook process with the generation currently
// selected by the installer. Reader must bypass the manager cache so a stale
// replica cannot admit a protected Pod during cache propagation.
type GenerationGate struct {
	Reader     client.Reader
	Namespace  string
	ConfigMap  string
	Generation string
}

func (g *GenerationGate) Check(ctx context.Context) error {
	if g == nil || g.Reader == nil || g.Namespace == "" || g.ConfigMap == "" || g.Generation == "" {
		return fmt.Errorf("admission generation gate is not configured")
	}
	var desired corev1.ConfigMap
	if err := g.Reader.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: g.ConfigMap}, &desired); err != nil {
		return fmt.Errorf("read desired admission generation: %w", err)
	}
	want := desired.Data[contract.AdmissionGenerationKey]
	if want == "" {
		return fmt.Errorf("desired admission generation is empty")
	}
	if want != g.Generation {
		return fmt.Errorf("webhook generation %q is stale; desired generation is %q", g.Generation, want)
	}
	return nil
}

func (g *GenerationGate) Readiness(request *http.Request) error {
	return g.Check(request.Context())
}
