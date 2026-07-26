// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package enrollment resolves the single stable Pod enrollment label to exact
// Pod and route UIDs without treating route readiness as enrollment.
package enrollment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const RouteLabel = "networking.waycloak.io/egress-route"

const (
	ReasonUnenrolled             = "Unenrolled"
	ReasonInvalidEnrollment      = "Invalid"
	ReasonRouteNotFound          = "RefNotFound"
	ReasonRouteNotReady          = "NotReady"
	ReasonObservationUnavailable = "ObservationUnavailable"
	ReasonReady                  = "Ready"
)

var ErrPodUIDMismatch = errors.New("pod UID does not match the exact CNI identity")

type Resolution struct {
	PodUID    types.UID
	RouteName string
	RouteUID  types.UID
	Enrolled  bool
	Ready     bool
	Reason    string
}

type Resolver struct{ Reader client.Reader }

func (r Resolver) Resolve(ctx context.Context, namespace, name string, uid types.UID) (Resolution, error) {
	if r.Reader == nil {
		return Resolution{}, errors.New("kubernetes reader is required")
	}
	pod := &corev1.Pod{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pod); err != nil {
		return Resolution{}, fmt.Errorf("read exact Pod identity: %w", err)
	}
	if pod.UID != uid {
		return Resolution{}, fmt.Errorf("%w: observed %q, expected %q", ErrPodUIDMismatch, pod.UID, uid)
	}
	result := Resolution{PodUID: pod.UID, Reason: ReasonUnenrolled}
	routeName, enrolled := pod.Labels[RouteLabel]
	if !enrolled {
		return result, nil
	}
	result.Enrolled = true
	result.RouteName = routeName
	if problems := validation.IsDNS1123Label(routeName); len(problems) != 0 {
		result.Reason = ReasonInvalidEnrollment
		return result, nil
	}

	route := &wayv1.VPNEgressRoute{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: routeName}, route); err != nil {
		if apierrors.IsNotFound(err) {
			result.Reason = ReasonRouteNotFound
			return result, nil
		}
		result.Reason = ReasonObservationUnavailable
		return result, fmt.Errorf("read enrolled route: %w", err)
	}
	result.RouteUID = route.UID
	if route.Status.ObservedGeneration != route.Generation {
		result.Reason = ReasonObservationUnavailable
		return result, nil
	}
	for _, conditionType := range []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady} {
		condition := apiMeta.FindStatusCondition(route.Status.Conditions, conditionType)
		if condition == nil || condition.ObservedGeneration != route.Generation {
			result.Reason = ReasonObservationUnavailable
			return result, nil
		}
		if condition.Status != metav1.ConditionTrue {
			result.Reason = condition.Reason
			if strings.TrimSpace(result.Reason) == "" {
				result.Reason = ReasonRouteNotReady
			}
			return result, nil
		}
	}
	result.Ready = true
	result.Reason = ReasonReady
	return result, nil
}

func HasAlphaAnnotation(annotations map[string]string) bool {
	for key := range annotations {
		if strings.HasPrefix(key, "networking.waycloak.io/") || strings.HasPrefix(key, "internal.networking.waycloak.io/") {
			return true
		}
	}
	return false
}
