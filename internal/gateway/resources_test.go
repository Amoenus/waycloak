// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"reflect"
	"strings"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDesiredStatefulSetIsSingletonAndIsolatesCredentials(t *testing.T) {
	gateway := testGateway()
	statefulSet := DesiredStatefulSet(gateway, WorkloadOptions{ManagerImage: digestImage("manager")})
	if statefulSet.Spec.Replicas == nil || *statefulSet.Spec.Replicas != 1 {
		t.Fatalf("replicas = %v", statefulSet.Spec.Replicas)
	}
	if statefulSet.Spec.Template.Spec.AutomountServiceAccountToken == nil || *statefulSet.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("gateway Pod received a Kubernetes API token")
	}
	engine := statefulSet.Spec.Template.Spec.Containers[0]
	manager := statefulSet.Spec.Template.Spec.Containers[1]
	if engine.Name != EngineContainer || manager.Name != ManagerContainer {
		t.Fatalf("containers = %s, %s", engine.Name, manager.Name)
	}
	if !hasMount(engine, "credentials") {
		t.Fatal("engine does not receive the credential Secret")
	}
	if hasMount(manager, "credentials") {
		t.Fatal("gateway manager receives provider credentials")
	}
	if engine.SecurityContext == nil || engine.SecurityContext.Capabilities == nil || !reflect.DeepEqual(engine.SecurityContext.Capabilities.Add, []corev1.Capability{"NET_ADMIN"}) {
		t.Fatalf("engine capabilities = %#v", engine.SecurityContext)
	}
	if manager.SecurityContext == nil || manager.SecurityContext.Capabilities == nil || !reflect.DeepEqual(manager.SecurityContext.Capabilities.Add, []corev1.Capability{"NET_ADMIN", "NET_BIND_SERVICE"}) {
		t.Fatalf("manager capabilities = %#v", manager.SecurityContext)
	}
	if manager.ReadinessProbe == nil || manager.ReadinessProbe.HTTPGet == nil || manager.ReadinessProbe.HTTPGet.Port.IntValue() != HealthPort {
		t.Fatalf("manager readiness probe = %#v", manager.ReadinessProbe)
	}
}

func TestResourceNameIsStableAndBounded(t *testing.T) {
	name := "gateway-with-a-deliberately-long-name-that-needs-to-be-shortened-for-owned-resources"
	first := ResourceName(name)
	if len(first) > 63 || first != ResourceName(name) || first == ResourceName(name+"-different") {
		t.Fatalf("resource name = %q", first)
	}
}

func TestGluetunUsesSecretFilesAndLoopbackReadOnlyControl(t *testing.T) {
	gateway := testGateway()
	gateway.Spec.Engine.Type = "Gluetun"
	gateway.Spec.Provider.Protocol = "openvpn"
	statefulSet := DesiredStatefulSet(gateway, WorkloadOptions{ManagerImage: digestImage("manager")})
	engine := statefulSet.Spec.Template.Spec.Containers[0]
	environment := map[string]string{}
	for _, variable := range engine.Env {
		environment[variable.Name] = variable.Value
	}
	for key, want := range map[string]string{
		"OPENVPN_USER_SECRETFILE":                  "/run/waycloak/credentials/username",
		"OPENVPN_PASSWORD_SECRETFILE":              "/run/waycloak/credentials/password",
		"HTTP_CONTROL_SERVER_ADDRESS":              "127.0.0.1:8000",
		"HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH": "/run/waycloak/engine-auth/config.toml",
		"VPN_INTERFACE":                            TunnelInterface,
		"FIREWALL_INPUT_PORTS":                     "18080",
	} {
		if environment[key] != want {
			t.Fatalf("environment %s = %q", key, environment[key])
		}
	}
	config := DesiredConfigMap(gateway, nil).Data[EngineAuthKey]
	if !strings.Contains(config, `routes = ["GET /v1/dns/status", "GET /v1/publicip/ip"]`) || strings.Contains(config, "PUT ") {
		t.Fatalf("control-server role is not read-only: %s", config)
	}
	postRules := DesiredConfigMap(gateway, nil).Data[EnginePostRulesKey]
	if !strings.Contains(postRules, "iptables --policy FORWARD ACCEPT") || !strings.Contains(postRules, "--in-interface "+OverlayInterfaceName(gateway.Name)) || !strings.Contains(postRules, "--source "+gateway.Spec.Overlay.CIDR) {
		t.Fatalf("Gluetun forwarding adapter is not gateway-scoped: %s", postRules)
	}
	if !hasMount(engine, "engine-firewall") {
		t.Fatal("Gluetun does not receive its forwarding-policy adapter")
	}
}

func hasMount(container corev1.Container, name string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == name {
			return true
		}
	}
	return false
}

func testGateway() *wayv1.VPNGateway {
	return &wayv1.VPNGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "egress"},
		Spec: wayv1.VPNGatewaySpec{
			Engine:   wayv1.EngineSpec{Type: "Test", Image: digestImage("engine")},
			Provider: wayv1.ProviderSpec{Name: "test", CredentialsSecretRef: corev1.LocalObjectReference{Name: "vpn-credentials"}},
			Overlay:  wayv1.OverlaySpec{CIDR: "172.30.99.0/24", VNI: 7999, MTU: 1320},
		},
	}
}

func digestImage(name string) string {
	return "registry.invalid/" + name + "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
