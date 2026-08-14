// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

var vpnGatewayGVR = schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpngateways"}

const gatewayRolloutTimeout = 5 * time.Minute

// ensureTargetGatewayPods closes the gap between an OnDelete StatefulSet's
// desired template and its live Pod. Release completion is not allowed while a
// running gateway still executes an older artifact.
func ensureTargetGatewayPods(ctx context.Context, clients *Clients, target ReleaseManifest) error {
	gateways, err := clients.Dynamic.Resource(vpnGatewayGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("inventory gateways for exact release rollout: %w", err)
	}
	sort.Slice(gateways.Items, func(i, j int) bool {
		left := gateways.Items[i].GetNamespace() + "/" + gateways.Items[i].GetName()
		right := gateways.Items[j].GetNamespace() + "/" + gateways.Items[j].GetName()
		return left < right
	})
	for index := range gateways.Items {
		gateway := &gateways.Items[index]
		if err := ensureTargetGatewayPod(ctx, clients, gateway, target); err != nil {
			return fmt.Errorf("roll gateway %s/%s to exact release: %w", gateway.GetNamespace(), gateway.GetName(), err)
		}
	}
	return nil
}

func ensureTargetGatewayPod(ctx context.Context, clients *Clients, gateway *unstructured.Unstructured, target ReleaseManifest) error {
	engine := target.Images["gluetun"].Repository + "@" + target.Images["gluetun"].Digest
	agent := target.Images["waycloak-gateway-agent"].Repository + "@" + target.Images["waycloak-gateway-agent"].Digest
	if !validExactImageReference(engine) || !validExactImageReference(agent) {
		return errors.New("target gateway image identity is incomplete")
	}

	var oldUID types.UID
	rolled := false
	err := wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, gatewayRolloutTimeout, true, func(ctx context.Context) (bool, error) {
		statefulSet, err := exactGatewayStatefulSet(ctx, clients, gateway)
		if err != nil {
			return false, err
		}
		if statefulSet == nil {
			// A rejected gateway has no runtime to roll. A gateway that still
			// claims Ready must not let a missing runtime pass release completion.
			current, retry, err := reobserveGateway(ctx, clients, gateway)
			if err != nil || retry {
				return false, err
			}
			return !gatewayCurrentReady(current), nil
		}
		if !gatewayTemplateIsTarget(&statefulSet.Spec.Template, engine, agent, target.Version, target.ManifestDigest) {
			return false, nil
		}
		pod, err := exactStatefulSetPod(ctx, clients, statefulSet)
		if err != nil {
			return false, err
		}
		if pod == nil {
			return false, nil
		}
		if !rolled && !gatewayPodIsTarget(pod, statefulSet, engine, agent, target.Version, target.ManifestDigest) {
			oldUID = pod.UID
			zero := int64(0)
			uid := pod.UID
			if err := clients.Kubernetes.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
				GracePeriodSeconds: &zero,
				Preconditions:      &metav1.Preconditions{UID: &uid},
			}); err != nil && !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("delete exact stale gateway Pod UID %s: %w", uid, err)
			}
			rolled = true
			return false, nil
		}
		if rolled && pod.UID == oldUID {
			return false, nil
		}
		if !gatewayPodIsTarget(pod, statefulSet, engine, agent, target.Version, target.ManifestDigest) {
			return false, nil
		}
		current, retry, err := reobserveGateway(ctx, clients, gateway)
		if err != nil || retry {
			return false, err
		}
		if !gatewayCurrentReady(current) {
			return false, nil
		}
		// Release completion proves the release-owned gateway data plane. A
		// workload binding can remain unready for application-local reasons
		// such as an unavailable volume and must not block a product upgrade.
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("wait for exact target gateway Pod and current Ready observation: %w", err)
	}
	return nil
}

func reobserveGateway(ctx context.Context, clients *Clients, gateway *unstructured.Unstructured) (*unstructured.Unstructured, bool, error) {
	current, err := clients.Dynamic.Resource(vpnGatewayGVR).Namespace(gateway.GetNamespace()).Get(ctx, gateway.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, errors.New("gateway was removed during exact release rollout")
	}
	if err != nil {
		// Transient API reads retry inside the bounded rollout deadline.
		return nil, true, nil //nolint:nilerr
	}
	if current.GetUID() != gateway.GetUID() {
		return nil, false, errors.New("gateway UID changed during exact release rollout")
	}
	return current, false, nil
}

func exactGatewayStatefulSet(ctx context.Context, clients *Clients, gateway *unstructured.Unstructured) (*appsv1.StatefulSet, error) {
	items, err := clients.Kubernetes.AppsV1().StatefulSets(gateway.GetNamespace()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var matched *appsv1.StatefulSet
	for index := range items.Items {
		item := &items.Items[index]
		for _, owner := range item.OwnerReferences {
			if owner.APIVersion == gateway.GetAPIVersion() && owner.Kind == gateway.GetKind() && owner.Name == gateway.GetName() && owner.UID == gateway.GetUID() && owner.Controller != nil && *owner.Controller {
				if matched != nil {
					return nil, errors.New("multiple StatefulSets own the exact gateway UID")
				}
				matched = item
			}
		}
	}
	return matched, nil
}

func exactStatefulSetPod(ctx context.Context, clients *Clients, statefulSet *appsv1.StatefulSet) (*corev1.Pod, error) {
	items, err := clients.Kubernetes.CoreV1().Pods(statefulSet.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var matched *corev1.Pod
	for index := range items.Items {
		item := &items.Items[index]
		for _, owner := range item.OwnerReferences {
			if owner.APIVersion == "apps/v1" && owner.Kind == "StatefulSet" && owner.Name == statefulSet.Name && owner.UID == statefulSet.UID && owner.Controller != nil && *owner.Controller {
				if matched != nil {
					return nil, errors.New("multiple Pods belong to the exact gateway StatefulSet UID")
				}
				matched = item
			}
		}
	}
	return matched, nil
}

func gatewayPodIsTarget(pod *corev1.Pod, statefulSet *appsv1.StatefulSet, engine, agent, version, manifestDigest string) bool {
	if pod.DeletionTimestamp != nil || !gatewayContainersMatch(pod.Spec.Containers, engine, agent) ||
		pod.Annotations["runtime.networking.waycloak.io/release-version"] != version ||
		pod.Annotations["runtime.networking.waycloak.io/release-manifest-digest"] != manifestDigest {
		return false
	}
	if statefulSet.Status.UpdateRevision == "" || pod.Labels[appsv1.StatefulSetRevisionLabel] != statefulSet.Status.UpdateRevision {
		return false
	}
	ready := map[string]bool{}
	for _, status := range pod.Status.ContainerStatuses {
		ready[status.Name] = status.Ready
	}
	return ready["vpn-engine"] && ready["gateway-agent"]
}

func gatewayTemplateIsTarget(template *corev1.PodTemplateSpec, engine, agent, version, manifestDigest string) bool {
	return gatewayContainersMatch(template.Spec.Containers, engine, agent) &&
		template.Annotations["runtime.networking.waycloak.io/release-version"] == version &&
		template.Annotations["runtime.networking.waycloak.io/release-manifest-digest"] == manifestDigest
}

func gatewayContainersMatch(containers []corev1.Container, engine, agent string) bool {
	images := map[string]string{}
	for _, container := range containers {
		images[container.Name] = container.Image
	}
	return images["vpn-engine"] == engine && images["gateway-agent"] == agent
}

func gatewayCurrentReady(gateway *unstructured.Unstructured) bool {
	return resourceCurrentReady(gateway)
}

func resourceCurrentReady(resource *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(resource.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		observed, _ := condition["observedGeneration"].(int64)
		if condition["type"] == "Ready" && condition["status"] == "True" && observed == resource.GetGeneration() {
			return true
		}
	}
	return false
}
