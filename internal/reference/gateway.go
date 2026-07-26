// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package reference implements target-side authorization for the replacement
// API without exposing referenced object existence before consent.
package reference

import (
	"context"
	"errors"
	"fmt"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type GatewayResolution struct {
	// Permitted is true only when the target owner has authorized the source
	// namespace. Gateway is nil for an authorized same-namespace reference to
	// a missing target, allowing the caller to report RefNotFound.
	Permitted bool
	Gateway   *wayv1.VPNGateway
}

type GatewayResolver interface {
	ResolveGateway(context.Context, string, wayv1.NamespacedObjectReference) (GatewayResolution, error)
}

// GatewayAuthorizer reads through an uncached API reader. Returning the
// authorized gateway snapshot prevents a second, stale cached read from
// undoing a consent revocation decision.
type GatewayAuthorizer struct {
	Reader client.Reader
}

func (a GatewayAuthorizer) ResolveGateway(ctx context.Context, sourceNamespace string, ref wayv1.NamespacedObjectReference) (GatewayResolution, error) {
	if a.Reader == nil {
		return GatewayResolution{}, errors.New("kubernetes API reader is required")
	}
	if sourceNamespace == "" || ref.Namespace == "" || ref.Name == "" {
		return GatewayResolution{}, errors.New("source namespace and gateway reference are required")
	}

	gateway := &wayv1.VPNGateway{}
	err := a.Reader.Get(ctx, client.ObjectKey{Namespace: string(ref.Namespace), Name: string(ref.Name)}, gateway)
	if sourceNamespace == string(ref.Namespace) {
		if apierrors.IsNotFound(err) {
			return GatewayResolution{Permitted: true}, nil
		}
		if err != nil {
			return GatewayResolution{}, fmt.Errorf("observe local gateway reference: %w", err)
		}
		return GatewayResolution{Permitted: true, Gateway: gateway}, nil
	}

	// Missing and non-consenting cross-namespace targets deliberately return
	// the same result so status cannot be used as an existence oracle.
	if apierrors.IsNotFound(err) {
		return GatewayResolution{}, nil
	}
	if err != nil {
		return GatewayResolution{}, fmt.Errorf("observe remote gateway consent: %w", err)
	}

	switch gateway.Spec.AllowedRoutes.Namespaces.From {
	case "", wayv1.RouteNamespaceSame:
		return GatewayResolution{}, nil
	case wayv1.RouteNamespaceAll:
		return GatewayResolution{Permitted: true, Gateway: gateway}, nil
	case wayv1.RouteNamespaceSelector:
		if gateway.Spec.AllowedRoutes.Namespaces.Selector == nil {
			return GatewayResolution{}, nil
		}
		selector, err := metav1.LabelSelectorAsSelector(gateway.Spec.AllowedRoutes.Namespaces.Selector)
		if err != nil {
			return GatewayResolution{}, nil
		}
		namespace := &corev1.Namespace{}
		if err := a.Reader.Get(ctx, client.ObjectKey{Name: sourceNamespace}, namespace); err != nil {
			return GatewayResolution{}, fmt.Errorf("observe source namespace policy labels: %w", err)
		}
		if !selector.Matches(labels.Set(namespace.Labels)) {
			return GatewayResolution{}, nil
		}
		return GatewayResolution{Permitted: true, Gateway: gateway}, nil
	default:
		return GatewayResolution{}, nil
	}
}
