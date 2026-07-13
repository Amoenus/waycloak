package controller

import (
	"context"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcilePersistsUIDBoundAllocationAcrossRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = wayv1.AddToScheme(scheme)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", UID: types.UID("pod-uid-1"), Annotations: map[string]string{contract.GatewayAnnotation: "egress/private", contract.InjectionVersionAnnotation: contract.InjectionVersion}}}
	gw := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "egress"}, Spec: wayv1.VPNGatewaySpec{Overlay: wayv1.OverlaySpec{CIDR: "172.30.99.0/29", VNI: 7999, MTU: 1320}, ClusterTraffic: wayv1.ClusterTrafficSpec{Mode: "Gateway"}}, Status: wayv1.VPNGatewayStatus{Overlay: wayv1.GatewayOverlayStatus{Endpoint: "10.42.0.2:4789", HealthPort: 18080}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&wayv1.VPNWorkload{}).WithObjects(pod, gw).Build()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "apps", Name: "app"}}
	r := &PodReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(20)}
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	name := contract.WorkloadName(string(pod.UID))
	var first wayv1.VPNWorkload
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: name}, &first); err != nil {
		t.Fatal(err)
	}
	if first.Status.Allocation.Address != "172.30.99.2" {
		t.Fatalf("allocation=%q", first.Status.Allocation.Address)
	}
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: contract.AllocationConfigMapName("apps", "app")}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["podUID"] != string(pod.UID) {
		t.Fatalf("ConfigMap UID=%q", cm.Data["podUID"])
	}
	for key, want := range map[string]string{"gatewayEndpoint": "10.42.0.2:4789", "gatewayHealthPort": "18080", "vni": "7999", "mtu": "1320", "clusterTrafficMode": "Gateway"} {
		if cm.Data[key] != want {
			t.Fatalf("ConfigMap %s=%q, want %q", key, cm.Data[key], want)
		}
	}
	restarted := &PodReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(20)}
	if _, err := restarted.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var after wayv1.VPNWorkload
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: name}, &after)
	if after.Status.Allocation.Address != first.Status.Allocation.Address {
		t.Fatalf("restart changed allocation: %s -> %s", first.Status.Allocation.Address, after.Status.Allocation.Address)
	}
}
