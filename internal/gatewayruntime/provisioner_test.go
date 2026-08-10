// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewayruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProvisionerCreatesCredentialIsolatedGatewayAndObservesExactPod(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "media", UID: "gateway-uid", Generation: 1}, Spec: wayv1.VPNGatewaySpec{GatewayClassName: "gluetun.waycloak.io", NativeConfigRefs: []wayv1.RoleObjectReference{{Role: waycontroller.GluetunEnvironmentRole, Name: "engine"}}, CredentialRefs: []wayv1.RoleObjectReference{{Role: waycontroller.OpenVPNCredentialsRole, Name: "credentials"}}, ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll}}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "engine", Namespace: "media", ResourceVersion: "1"}, Data: map[string]string{"VPN_SERVICE_PROVIDER": "protonvpn", "VPN_TYPE": "openvpn"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "media", ResourceVersion: "1"}, Data: map[string][]byte{"username": []byte("CANARY-USERNAME"), "password": []byte("CANARY-PASSWORD")}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Pod{}).WithObjects(gateway, config, secret).Build()
	provisioner := fixture(kube)
	observation, err := provisioner.Reconcile(context.Background(), gateway)
	if err != nil || !observation.Programmed || observation.Ready {
		t.Fatalf("unexpected initial observation: %#v %v", observation, err)
	}
	statefulSet := &appsv1.StatefulSet{}
	must(t, kube.Get(context.Background(), client.ObjectKey{Namespace: "media", Name: "waycloak-gateway-private"}, statefulSet))
	statefulSet.UID = "statefulset-uid"
	must(t, kube.Update(context.Background(), statefulSet))
	if statefulSet.Spec.Template.Spec.AutomountServiceAccountToken == nil || *statefulSet.Spec.Template.Spec.AutomountServiceAccountToken || len(statefulSet.Spec.Template.Spec.Containers) != 2 {
		t.Fatalf("unsafe gateway Pod identity: %#v", statefulSet.Spec.Template.Spec)
	}
	engine, agent := statefulSet.Spec.Template.Spec.Containers[0], statefulSet.Spec.Template.Spec.Containers[1]
	if engine.Name != "vpn-engine" || len(engine.Env) != 2 || agent.Name != "gateway-agent" || len(agent.Env) != 0 || len(agent.VolumeMounts) != 0 {
		t.Fatalf("credential boundary is unsafe: engine=%#v agent=%#v", engine, agent)
	}
	if engine.SecurityContext == nil || engine.SecurityContext.Capabilities == nil || len(engine.SecurityContext.Capabilities.Add) != 2 || engine.SecurityContext.Capabilities.Add[0] != "NET_ADMIN" || engine.SecurityContext.Capabilities.Add[1] != "CHOWN" {
		t.Fatalf("VPN engine lacks its exact runtime capabilities: %#v", engine.SecurityContext)
	}
	if agent.SecurityContext == nil || agent.SecurityContext.Capabilities == nil || len(agent.SecurityContext.Capabilities.Add) != 1 || agent.SecurityContext.Capabilities.Add[0] != "NET_ADMIN" {
		t.Fatalf("gateway agent capabilities were broadened: %#v", agent.SecurityContext)
	}
	rendered, _ := json.Marshal(statefulSet)
	if bytes.Contains(rendered, []byte("CANARY-USERNAME")) || bytes.Contains(rendered, []byte("CANARY-PASSWORD")) {
		t.Fatal("gateway workload copied credential values")
	}
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "waycloak-gateway-private-0", Namespace: "media", Labels: statefulSet.Spec.Template.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: statefulSet.Name, UID: statefulSet.UID, Controller: &controller}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.42.0.20", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	status := pod.Status
	pod.Status = corev1.PodStatus{}
	must(t, kube.Create(context.Background(), pod))
	pod.Status = status
	must(t, kube.Status().Update(context.Background(), pod))
	observation, err = provisioner.Reconcile(context.Background(), gateway)
	if err != nil || !observation.Ready || !hasAddress(observation.Addresses, wayv1.GatewayAddressTypeUnderlayEndpoint, "10.42.0.20:4789") {
		t.Fatalf("exact ready Pod was not observed: %#v %v", observation, err)
	}
}

func TestProvisionerRejectsMutableImageBeforeCreatingObjects(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	provisioner := fixture(kube)
	provisioner.AgentImage = "example.invalid/agent:latest"
	if _, err := provisioner.Reconcile(context.Background(), &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "default", UID: "uid"}}); err == nil {
		t.Fatal("mutable gateway agent image accepted")
	}
}

func fixture(kube client.Client) *Provisioner {
	return &Provisioner{Client: kube, Reader: kube, EngineImage: "docker.io/qmcgaw/gluetun@sha256:" + strings.Repeat("a", 64), AgentImage: "ghcr.io/amoenus/waycloak-gateway-agent@sha256:" + strings.Repeat("b", 64), OverlayCIDR: netip.MustParsePrefix("100.96.0.0/24"), VNI: 7999, MTU: 1320, VXLANPort: 4789, HealthPort: 18080, HTTPClient: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
	})}}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (function roundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func hasAddress(values []wayv1.GatewayAddress, addressType wayv1.QualifiedName, wanted string) bool {
	for _, value := range values {
		if value.Type == addressType && value.Value == wanted {
			return true
		}
	}
	return false
}
