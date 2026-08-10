// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestDoctorScopesCapabilitiesToInstalledNodeSelection(t *testing.T) {
	clients := doctorClients(t,
		doctorNode("amd64", "amd64", true),
		doctorNode("arm64-a", "arm64", false),
		doctorNode("arm64-b", "arm64", false),
		doctorDaemonSet(cniInstallerComponent, map[string]string{"kubernetes.io/arch": "amd64"}),
		doctorDaemonSet(nodeAgentComponent, map[string]string{"kubernetes.io/arch": "amd64"}),
	)

	report, err := Doctor(context.Background(), clients, "", "")
	if err != nil || !report.Healthy {
		t.Fatalf("selected healthy row was not healthy: %#v %v", report, err)
	}
	if report.Nodes["CNICapable"] != 1 || report.Nodes["NotSelected"] != 2 || report.Nodes["Unavailable"] != 0 {
		t.Fatalf("unexpected selected-node states: %#v", report.Nodes)
	}
}

func TestDoctorFailsWhenSelectedNodeCapabilityIsUnavailable(t *testing.T) {
	clients := doctorClients(t,
		doctorNode("amd64", "amd64", false),
		doctorNode("arm64", "arm64", true),
		doctorDaemonSet(cniInstallerComponent, map[string]string{"kubernetes.io/arch": "amd64"}),
		doctorDaemonSet(nodeAgentComponent, map[string]string{"kubernetes.io/arch": "amd64"}),
	)

	report, err := Doctor(context.Background(), clients, "", "")
	if err != nil || report.Healthy || report.Nodes["Unavailable"] != 1 || report.Nodes["NotSelected"] != 1 {
		t.Fatalf("selected capability loss did not fail closed: %#v %v", report, err)
	}
	if !doctorProblem(report, "One or more nodes lack a current authenticated Core capability") {
		t.Fatalf("selected capability loss lacks a precise problem: %#v", report.Problems)
	}
}

func TestDoctorFailsClosedWhenNodeComponentSelectorsDisagree(t *testing.T) {
	clients := doctorClients(t,
		doctorNode("amd64", "amd64", true),
		doctorNode("arm64", "arm64", true),
		doctorDaemonSet(cniInstallerComponent, map[string]string{"kubernetes.io/arch": "amd64"}),
		doctorDaemonSet(nodeAgentComponent, map[string]string{"kubernetes.io/arch": "arm64"}),
	)

	report, err := Doctor(context.Background(), clients, "", "")
	if err != nil || report.Healthy || !doctorProblem(report, "Waycloak CNI installer and node agent select different nodes") {
		t.Fatalf("incoherent installation selection was accepted: %#v %v", report, err)
	}
}

func TestDoctorEmptySelectionRequiresCapabilityFromEveryNode(t *testing.T) {
	clients := doctorClients(t,
		doctorNode("ready", "amd64", true),
		doctorNode("unavailable", "arm64", false),
		doctorDaemonSet(cniInstallerComponent, nil),
		doctorDaemonSet(nodeAgentComponent, nil),
	)

	report, err := Doctor(context.Background(), clients, "", "")
	if err != nil || report.Healthy || report.Nodes["CNICapable"] != 1 || report.Nodes["Unavailable"] != 1 || report.Nodes["NotSelected"] != 0 {
		t.Fatalf("unconstrained installation did not require every node: %#v %v", report, err)
	}
}

func TestDoctorFailsWhenNoNodeMatchesInstalledSelection(t *testing.T) {
	clients := doctorClients(t,
		doctorNode("arm64", "arm64", true),
		doctorDaemonSet(cniInstallerComponent, map[string]string{"kubernetes.io/arch": "amd64"}),
		doctorDaemonSet(nodeAgentComponent, map[string]string{"kubernetes.io/arch": "amd64"}),
	)

	report, err := Doctor(context.Background(), clients, "", "")
	if err != nil || report.Healthy || report.Nodes["NotSelected"] != 1 || !doctorProblem(report, "No nodes match the installed Waycloak node selection") {
		t.Fatalf("empty selected node set was accepted: %#v %v", report, err)
	}
}

func TestDoctorFailsWhenNodeComponentsAreMissing(t *testing.T) {
	clients := doctorClients(t,
		doctorNode("amd64", "amd64", true),
		doctorDaemonSet(nodeAgentComponent, map[string]string{"kubernetes.io/arch": "amd64"}),
	)

	report, err := Doctor(context.Background(), clients, "", "")
	if err != nil || report.Healthy || !doctorProblem(report, "Exactly one Waycloak CNI installer and node agent must be observable") {
		t.Fatalf("incomplete node installation was accepted: %#v %v", report, err)
	}
}

func doctorClients(t *testing.T, objects ...runtime.Object) *Clients {
	t.Helper()
	clients := supportedClients(t)
	clients.Kubernetes = kubernetesfake.NewSimpleClientset(objects...)
	resource := &unstructured.Unstructured{}
	resource.SetAPIVersion("networking.waycloak.io/v1beta1")
	resource.SetKind("VPNGatewayClass")
	resource.SetName("gluetun.waycloak.io")
	resource.SetGeneration(1)
	resource.Object["status"] = map[string]any{"conditions": []any{map[string]any{
		"type": "Ready", "status": "True", "reason": "Ready", "observedGeneration": int64(1),
	}}}
	if _, err := clients.Dynamic.Resource(doctorResources[0].GVR).Create(context.Background(), resource, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return clients
}

func doctorNode(name, architecture string, capable bool) *corev1.Node {
	labels := map[string]string{"kubernetes.io/arch": architecture}
	if capable {
		labels[coreReadyNodeLabel] = "true"
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func doctorDaemonSet(component string, selector map[string]string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "waycloak-" + component,
			Namespace: "waycloak-system",
			Labels: map[string]string{
				componentLabel: component,
				instanceLabel:  "waycloak",
			},
		},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeSelector: selector}}},
	}
}

func doctorProblem(report DoctorReport, wanted string) bool {
	for _, problem := range report.Problems {
		if problem == wanted {
			return true
		}
	}
	return false
}
