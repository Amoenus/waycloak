package admission

import (
	"context"
	"reflect"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testAgentImage = "registry.example/waycloak-agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func testMutator(t *testing.T, selector metav1.LabelSelector) *PodMutator {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = wayv1.AddToScheme(scheme)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"waycloak": "allowed"}}}
	gw := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "egress"}, Spec: wayv1.VPNGatewaySpec{WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: selector}}}
	return &PodMutator{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns, gw).Build(), Scheme: scheme, AgentImage: testAgentImage}
}

func TestUnannotatedPodCompletelyUnchanged(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "apps"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app"}}}}
	before := pod.DeepCopy()
	changed, err := m.Mutate(context.Background(), pod)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !reflect.DeepEqual(before, pod) {
		t.Fatal("unannotated Pod changed")
	}
}

func TestAnnotatedMutationIsDeterministicAndIdempotent(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{MatchLabels: map[string]string{"waycloak": "allowed"}})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app"}}}}
	originalApp := pod.Spec.Containers[0].DeepCopy()
	changed, err := m.Mutate(context.Background(), pod)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	after := pod.DeepCopy()
	changed, err = m.Mutate(context.Background(), pod)
	if err != nil || changed {
		t.Fatalf("second changed=%v err=%v", changed, err)
	}
	if !reflect.DeepEqual(after, pod) {
		t.Fatal("second mutation changed Pod")
	}
	if !reflect.DeepEqual(originalApp, &pod.Spec.Containers[0]) {
		t.Fatal("application container was modified")
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("token automount not disabled")
	}
	if pod.Spec.Volumes[len(pod.Spec.Volumes)-1].ConfigMap.Optional == nil || *pod.Spec.Volumes[len(pod.Spec.Volumes)-1].ConfigMap.Optional {
		t.Fatal("allocation volume must be required")
	}
}

func TestUnauthorizedGatewayRejected(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{MatchLabels: map[string]string{"other": "yes"}})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}}
	_, err := m.Mutate(context.Background(), pod)
	r, ok := err.(*Rejection)
	if !ok || r.Reason != "UnauthorizedGateway" {
		t.Fatalf("got %#v", err)
	}
}

func TestExplicitServiceAccountTokenRejected(t *testing.T) {
	yes := true
	m := testMutator(t, metav1.LabelSelector{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &yes}}
	_, err := m.Mutate(context.Background(), pod)
	r, ok := err.(*Rejection)
	if !ok || r.Reason != "ApplicationCredentialsForbidden" {
		t.Fatalf("got %#v", err)
	}
}
