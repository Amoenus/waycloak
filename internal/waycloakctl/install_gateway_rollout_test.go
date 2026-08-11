// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestEnsureTargetGatewayPodsReplacesStaleOnDeletePod(t *testing.T) {
	clients := supportedClients(t)
	target := releaseManifest()
	engine := target.Images["gluetun"].Repository + "@" + target.Images["gluetun"].Digest
	agent := target.Images["waycloak-gateway-agent"].Repository + "@" + target.Images["waycloak-gateway-agent"].Digest
	controller := true
	gateway := rolloutGateway("gateway-uid")
	if _, err := clients.Dynamic.Resource(vpnGatewayGVR).Namespace("media").Create(context.Background(), gateway, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-runtime", Namespace: "media", UID: "statefulset-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: gateway.GetAPIVersion(), Kind: gateway.GetKind(), Name: gateway.GetName(), UID: gateway.GetUID(), Controller: &controller}}},
		Spec:       appsv1.StatefulSetSpec{Template: gatewayTemplate(target, engine, agent)},
		Status:     appsv1.StatefulSetStatus{UpdateRevision: "target-revision"},
	}
	if _, err := clients.Kubernetes.AppsV1().StatefulSets("media").Create(context.Background(), statefulSet, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	old := rolloutPod("gateway-runtime-0", "old-pod-uid", statefulSet, "old-revision", "old", "sha256:"+repeatHex("c"), "registry.invalid/old@sha256:"+repeatHex("a"), "registry.invalid/old@sha256:"+repeatHex("b"))
	if _, err := clients.Kubernetes.CoreV1().Pods("media").Create(context.Background(), old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	clients.Kubernetes.(*kubernetesfake.Clientset).PrependReactor("delete", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		deleted := action.(clienttesting.DeleteAction)
		if deleted.GetName() != old.Name {
			t.Fatalf("deleted unexpected Pod %s", deleted.GetName())
		}
		options := deleted.GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != old.UID {
			t.Fatalf("gateway deletion lacked the exact Pod UID precondition: %#v", options.Preconditions)
		}
		tracker := clients.Kubernetes.(*kubernetesfake.Clientset).Tracker()
		if err := tracker.Delete(corev1.SchemeGroupVersion.WithResource("pods"), "media", old.Name); err != nil {
			return true, nil, err
		}
		fresh := rolloutPod(old.Name, "target-pod-uid", statefulSet, "target-revision", target.Version, target.ManifestDigest, engine, agent)
		if err := tracker.Create(corev1.SchemeGroupVersion.WithResource("pods"), fresh, "media"); err != nil {
			return true, nil, err
		}
		return true, nil, nil
	})

	if err := ensureTargetGatewayPods(context.Background(), clients, target); err != nil {
		t.Fatal(err)
	}
	current, err := clients.Kubernetes.CoreV1().Pods("media").Get(context.Background(), old.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if current.UID != "target-pod-uid" || !gatewayContainersMatch(current.Spec.Containers, engine, agent) {
		t.Fatalf("stale gateway Pod was not replaced with exact target: %#v", current)
	}
}

func TestEnsureTargetGatewayPodsDoesNotReplaceCurrentPod(t *testing.T) {
	clients := supportedClients(t)
	target := releaseManifest()
	engine := target.Images["gluetun"].Repository + "@" + target.Images["gluetun"].Digest
	agent := target.Images["waycloak-gateway-agent"].Repository + "@" + target.Images["waycloak-gateway-agent"].Digest
	controller := true
	gateway := rolloutGateway("gateway-uid")
	if _, err := clients.Dynamic.Resource(vpnGatewayGVR).Namespace("media").Create(context.Background(), gateway, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-runtime", Namespace: "media", UID: "statefulset-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: gateway.GetAPIVersion(), Kind: gateway.GetKind(), Name: gateway.GetName(), UID: gateway.GetUID(), Controller: &controller}}},
		Spec:       appsv1.StatefulSetSpec{Template: gatewayTemplate(target, engine, agent)},
		Status:     appsv1.StatefulSetStatus{UpdateRevision: "target-revision"},
	}
	if _, err := clients.Kubernetes.AppsV1().StatefulSets("media").Create(context.Background(), statefulSet, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	current := rolloutPod("gateway-runtime-0", "target-pod-uid", statefulSet, "target-revision", target.Version, target.ManifestDigest, engine, agent)
	if _, err := clients.Kubernetes.CoreV1().Pods("media").Create(context.Background(), current, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTargetGatewayPods(context.Background(), clients, target); err != nil {
		t.Fatal(err)
	}
	for _, action := range clients.Kubernetes.(*kubernetesfake.Clientset).Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "pods" {
			t.Fatal("current exact gateway Pod was deleted")
		}
	}
}

func TestGatewayBindingsMustRecoverBeforeReleaseCompletion(t *testing.T) {
	clients := supportedClients(t)
	gateway := rolloutGateway("gateway-uid")
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.waycloak.io/v1beta1",
		"kind":       "VPNWorkloadBinding",
		"metadata":   map[string]any{"name": "pod-binding", "namespace": "media", "generation": int64(2)},
		"spec": map[string]any{"gatewayRef": map[string]any{
			"name": gateway.GetName(), "namespace": gateway.GetNamespace(), "uid": string(gateway.GetUID()),
		}},
		"status": map[string]any{"conditions": []any{map[string]any{
			"type": "Ready", "status": "False", "reason": "NotReady", "observedGeneration": int64(2),
		}}},
	}}
	created, err := clients.Dynamic.Resource(vpnWorkloadBindingGVR).Namespace("media").Create(context.Background(), binding, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := gatewayBindingsCurrentReady(context.Background(), clients, gateway)
	if err != nil || ready {
		t.Fatalf("unready exact gateway binding passed release completion: ready=%t err=%v", ready, err)
	}
	conditions, _, _ := unstructured.NestedSlice(created.Object, "status", "conditions")
	conditions[0].(map[string]any)["status"] = "True"
	created.Object["status"].(map[string]any)["conditions"] = conditions
	if _, err = clients.Dynamic.Resource(vpnWorkloadBindingGVR).Namespace("media").Update(context.Background(), created, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	ready, err = gatewayBindingsCurrentReady(context.Background(), clients, gateway)
	if err != nil || !ready {
		t.Fatalf("current Ready exact gateway binding blocked release completion: ready=%t err=%v", ready, err)
	}
}

func TestReadyGatewayWithoutRuntimeCannotPassReleaseCompletion(t *testing.T) {
	clients := supportedClients(t)
	gateway := rolloutGateway("gateway-uid")
	if _, err := clients.Dynamic.Resource(vpnGatewayGVR).Namespace("media").Create(context.Background(), gateway, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := ensureTargetGatewayPods(ctx, clients, releaseManifest()); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ready gateway without a runtime passed release completion: %v", err)
	}
}

func rolloutGateway(uid k8stypes.UID) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.waycloak.io/v1beta1",
		"kind":       "VPNGateway",
		"metadata":   map[string]any{"name": "private", "namespace": "media", "uid": string(uid), "generation": int64(1)},
		"status": map[string]any{"conditions": []any{map[string]any{
			"type": "Ready", "status": "True", "reason": "Ready", "observedGeneration": int64(1),
		}}},
	}}
}

func rolloutPod(name string, uid k8stypes.UID, statefulSet *appsv1.StatefulSet, revision, version, manifestDigest, engine, agent string) *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: statefulSet.Namespace, UID: uid, Labels: map[string]string{appsv1.StatefulSetRevisionLabel: revision}, Annotations: map[string]string{"runtime.networking.waycloak.io/release-version": version, "runtime.networking.waycloak.io/release-manifest-digest": manifestDigest}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: statefulSet.Name, UID: statefulSet.UID, Controller: &controller}}},
		Spec:       corev1.PodSpec{Containers: gatewayContainers(engine, agent)},
		Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "vpn-engine", Ready: true}, {Name: "gateway-agent", Ready: true}}},
	}
}

func gatewayTemplate(target ReleaseManifest, engine, agent string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"runtime.networking.waycloak.io/release-version": target.Version, "runtime.networking.waycloak.io/release-manifest-digest": target.ManifestDigest}},
		Spec:       corev1.PodSpec{Containers: gatewayContainers(engine, agent)},
	}
}

func gatewayContainers(engine, agent string) []corev1.Container {
	return []corev1.Container{{Name: "vpn-engine", Image: engine}, {Name: "gateway-agent", Image: agent}}
}

func repeatHex(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}
