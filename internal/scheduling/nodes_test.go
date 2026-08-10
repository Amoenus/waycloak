// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package scheduling

import (
	"context"
	"strconv"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/nodeagent"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPublisherUsesExactAuthenticatedNodeAndPreservesForeignLabels(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	kube := fakeClient(t, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"topology.kubernetes.io/zone": "test"}}})
	publisher := fixturePublisher(kube, now)
	if err := publisher.Apply(context.Background(), agentPod("node-a"), validReport("node-a", now)); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{}
	if err := kube.Get(context.Background(), client.ObjectKey{Name: "node-a"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[CoreReadyLabel] != "true" || node.Labels[CapabilityEpochLabel] != "2000" || node.Labels[ObservationEpochLabel] != "2000000000000" || node.Labels["topology.kubernetes.io/zone"] != "test" {
		t.Fatalf("published labels = %#v", node.Labels)
	}
}

func TestPublisherWithdrawsSkewedOrUnsupportedReportsAndRejectsForeignNode(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	tests := map[string]func(*nodeagent.NodeReport){
		"missing capability": func(report *nodeagent.NodeReport) { report.Capabilities = report.Capabilities[1:] },
		"release skew":       func(report *nodeagent.NodeReport) { report.ReleaseIdentity.Version = "v2.0.0" },
		"stale clock":        func(report *nodeagent.NodeReport) { report.ObservedAt = now.Add(-2 * time.Minute) },
		"not ready":          func(report *nodeagent.NodeReport) { report.Ready = false },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			kube := fakeClient(t, readyNode("node-a", now))
			publisher := fixturePublisher(kube, now)
			report := validReport("node-a", now)
			mutate(&report)
			err := publisher.Apply(context.Background(), agentPod("node-a"), report)
			if name != "not ready" && err == nil {
				t.Fatal("invalid report was accepted")
			}
			node := &corev1.Node{}
			if getErr := kube.Get(context.Background(), client.ObjectKey{Name: "node-a"}, node); getErr != nil {
				t.Fatal(getErr)
			}
			if node.Labels[CoreReadyLabel] != "" || node.Labels[CapabilityEpochLabel] != "" {
				t.Fatalf("invalid report retained readiness: %#v", node.Labels)
			}
			if name == "not ready" && node.Labels[ObservationEpochLabel] != "2000000000000" {
				t.Fatalf("authenticated held report lacked non-ready observation evidence: %#v", node.Labels)
			}
		})
	}
	kube := fakeClient(t, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b"}})
	if err := fixturePublisher(kube, now).Apply(context.Background(), agentPod("node-a"), validReport("node-b", now)); err == nil {
		t.Fatal("authenticated node-a agent published node-b capability")
	}
}

func TestNodeCapabilityReconcilerWithdrawsStaleLabels(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	kube := fakeClient(t, readyNode("stale", now.Add(-time.Minute)), readyNode("fresh", now))
	reconciler := &NodeCapabilityReconciler{Client: kube, Now: func() time.Time { return now }, Freshness: 20 * time.Second}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "stale"}}); err != nil {
		t.Fatal(err)
	}
	stale := &corev1.Node{}
	if err := kube.Get(context.Background(), client.ObjectKey{Name: "stale"}, stale); err != nil {
		t.Fatal(err)
	}
	if stale.Labels[CoreReadyLabel] != "" {
		t.Fatalf("stale readiness remained: %#v", stale.Labels)
	}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "fresh"}})
	if err != nil || result.RequeueAfter <= 0 {
		t.Fatalf("fresh reconciliation = %#v, %v", result, err)
	}
}

func fixturePublisher(kube client.Client, now time.Time) Publisher {
	return Publisher{Client: kube, ReleaseIdentity: releaseIdentity(), ConformanceProfile: "networking.waycloak.io/Core-v1", Now: func() time.Time { return now }}
}

func validReport(nodeName string, now time.Time) nodeagent.NodeReport {
	return nodeagent.NodeReport{NodeName: nodeName, NodeBootID: "boot", InstanceID: "instance", ObservedAt: now, Ready: true,
		Capabilities: append([]string(nil), CoreCapabilities...), ReleaseIdentity: releaseIdentity(), ConformanceProfile: "networking.waycloak.io/Core-v1"}
}

func releaseIdentity() wayv1.ReleaseIdentity {
	return wayv1.ReleaseIdentity{Version: "v1.0.0-beta.1", ManifestDigest: "sha256:4444444444444444444444444444444444444444444444444444444444444444"}
}

func agentPod(nodeName string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "waycloak-system", UID: "agent-uid"}, Spec: corev1.PodSpec{NodeName: nodeName}}
}

func readyNode(name string, observed time.Time) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{CoreReadyLabel: "true", CapabilityEpochLabel: strconv.FormatInt(observed.Unix(), 10)}}}
}

func fakeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
