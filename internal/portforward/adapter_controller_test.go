// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWorkloadAdapterRequiresOneExactCredentialFreeHealthyPod(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	adapter := &wayv1.WorkloadAdapter{ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent", Namespace: "apps", Generation: 1}, Spec: wayv1.WorkloadAdapterSpec{
		Image: "registry.invalid/adapter@sha256:" + strings.Repeat("a", 64), ProtocolVersion: AdapterAPIVersion, SupportedApplications: []wayv1.QualifiedName{"example.io/qbittorrent"},
	}}
	no := false
	yes := true
	port := int32(DefaultAdapterPort)
	protocol := corev1.ProtocolTCP
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "adapter-pod", Namespace: adapter.Namespace, UID: "adapter-pod-uid"}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: &no, Containers: []corev1.Container{{Name: "adapter", Image: adapter.Spec.Image,
			Ports: []corev1.ContainerPort{{Name: "https", ContainerPort: port, Protocol: corev1.ProtocolTCP}}, SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &no, RunAsNonRoot: &yes, ReadOnlyRootFilesystem: &yes,
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			}}},
	}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: AdapterServiceName("apps", "qbittorrent"), Namespace: adapter.Namespace, UID: "adapter-service-uid"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "https", Port: port, Protocol: corev1.ProtocolTCP}}}}
	controller := true
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "adapter-slice", Namespace: adapter.Namespace,
		Labels: map[string]string{discoveryv1.LabelServiceName: service.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID, Controller: &controller}}},
		AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Port: &port, Protocol: &protocol}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.42.0.20"}, Conditions: discoveryv1.EndpointConditions{Ready: &yes},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: adapter.Namespace, Name: pod.Name, UID: pod.UID}}}}
	kube := adapterClient(t, adapter, pod, service, slice)
	health := &fakeAdapterHealth{now: now, podUID: wayv1.ObjectUID(pod.UID), ready: true}
	reconciler := &WorkloadAdapterReconciler{Client: kube, APIReader: kube, Health: health, Now: func() time.Time { return now }}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(adapter)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	current := &wayv1.WorkloadAdapter{}
	if err := kube.Get(context.Background(), request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	for _, conditionType := range adapterConditionOrder {
		condition := apiMeta.FindStatusCondition(current.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != current.Generation {
			t.Fatalf("condition %s = %#v", conditionType, condition)
		}
	}
	resourceVersion := current.ResourceVersion
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if current.ResourceVersion != resourceVersion {
		t.Fatalf("no-op adapter reconciliation wrote object: %s -> %s", resourceVersion, current.ResourceVersion)
	}

	pod.Spec.AutomountServiceAccountToken = &yes
	if err := kube.Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	resolved := apiMeta.FindStatusCondition(current.Status.Conditions, wayv1.ConditionResolvedRefs)
	readyCondition := apiMeta.FindStatusCondition(current.Status.Conditions, wayv1.ConditionReady)
	if resolved == nil || resolved.Status != metav1.ConditionFalse || resolved.Reason != wayv1.ReasonIncompatibleRef || readyCondition == nil || readyCondition.Status != metav1.ConditionFalse {
		t.Fatalf("unsafe adapter status = %#v", current.Status)
	}
}

type fakeAdapterHealth struct {
	now    time.Time
	podUID wayv1.ObjectUID
	ready  bool
}

func (h *fakeAdapterHealth) Observe(_ context.Context, namespace wayv1.NamespaceName, name wayv1.ObjectName, image string) (AdapterHealthObservation, error) {
	return AdapterHealthObservation{APIVersion: AdapterAPIVersion, Namespace: namespace, Name: name, Image: image, PodUID: h.podUID, ObservedAt: h.now, Ready: h.ready}, nil
}

func adapterClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, discoveryv1.AddToScheme, wayv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&wayv1.WorkloadAdapter{}).WithObjects(objects...).Build()
}
