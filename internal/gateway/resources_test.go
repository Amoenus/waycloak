// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"reflect"
	"strings"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDesiredStatefulSetIsSingletonAndIsolatesCredentials(t *testing.T) {
	gateway := testGateway()
	statefulSet := DesiredStatefulSet(gateway, WorkloadOptions{ManagerImage: digestImage("manager")})
	if statefulSet.Spec.Replicas == nil || *statefulSet.Spec.Replicas != 1 {
		t.Fatalf("replicas = %v", statefulSet.Spec.Replicas)
	}
	if statefulSet.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
		t.Fatalf("gateway update strategy = %q", statefulSet.Spec.UpdateStrategy.Type)
	}
	if statefulSet.Spec.Template.Spec.AutomountServiceAccountToken == nil || *statefulSet.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("gateway Pod received a Kubernetes API token")
	}
	engine := statefulSet.Spec.Template.Spec.Containers[0]
	manager := statefulSet.Spec.Template.Spec.Containers[1]
	if len(statefulSet.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("gateway init containers = %#v", statefulSet.Spec.Template.Spec.InitContainers)
	}
	renderer := statefulSet.Spec.Template.Spec.InitContainers[0]
	if renderer.Name != FirewallRendererContainer || renderer.Image != digestImage("manager") || hasMount(renderer, "credentials") || renderer.SecurityContext == nil || renderer.SecurityContext.Capabilities == nil || !reflect.DeepEqual(renderer.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}) || len(renderer.SecurityContext.Capabilities.Add) != 0 {
		t.Fatalf("engine firewall renderer isolation = %#v", renderer)
	}
	if !hasMount(renderer, "runtime") || !containsArgument(renderer.Args, "--resolver-output=/run/waycloak/runtime/resolv.conf") {
		t.Fatalf("engine firewall renderer does not persist the pre-engine resolver observation: %#v", renderer)
	}
	if engine.Name != EngineContainer || manager.Name != ManagerContainer {
		t.Fatalf("containers = %s, %s", engine.Name, manager.Name)
	}
	if !hasMount(engine, "credentials") {
		t.Fatal("engine does not receive the credential Secret")
	}
	if hasMount(manager, "credentials") {
		t.Fatal("gateway manager receives provider credentials")
	}
	if engine.SecurityContext == nil || engine.SecurityContext.Capabilities == nil || !reflect.DeepEqual(engine.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}) || !reflect.DeepEqual(engine.SecurityContext.Capabilities.Add, []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "NET_ADMIN", "SETGID", "SETUID"}) {
		t.Fatalf("engine capabilities = %#v", engine.SecurityContext)
	}
	if manager.SecurityContext == nil || manager.SecurityContext.Capabilities == nil || !reflect.DeepEqual(manager.SecurityContext.Capabilities.Add, []corev1.Capability{"NET_ADMIN"}) {
		t.Fatalf("manager capabilities = %#v", manager.SecurityContext)
	}
	if manager.ReadinessProbe == nil || manager.ReadinessProbe.HTTPGet == nil || manager.ReadinessProbe.HTTPGet.Port.IntValue() != HealthPort {
		t.Fatalf("manager readiness probe = %#v", manager.ReadinessProbe)
	}
	if !containsArgument(manager.Args, "--resolv-conf=/run/waycloak/runtime/resolv.conf") {
		t.Fatalf("gateway manager does not consume the captured resolver observation: %#v", manager.Args)
	}
}

func TestResourceNameIsStableAndBounded(t *testing.T) {
	name := "gateway-with-a-deliberately-long-name-that-needs-to-be-shortened-for-owned-resources"
	first := ResourceName(name)
	if len(first) > 63 || first != ResourceName(name) || first == ResourceName(name+"-different") {
		t.Fatalf("resource name = %q", first)
	}
}

func TestDesiredPodDisruptionBudgetProtectsSingleton(t *testing.T) {
	gateway := testGateway()
	pdb := DesiredPodDisruptionBudget(gateway)
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 1 {
		t.Fatalf("gateway minAvailable = %#v", pdb.Spec.MinAvailable)
	}
	if pdb.Spec.Selector == nil || !reflect.DeepEqual(pdb.Spec.Selector.MatchLabels, SelectorLabels(gateway)) {
		t.Fatalf("gateway disruption selector = %#v", pdb.Spec.Selector)
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
		"SERVER_COUNTRIES":                         gateway.Spec.Provider.Region,
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
	if !strings.Contains(postRules, "iptables --policy FORWARD ACCEPT") || !strings.Contains(postRules, "--protocol udp --destination-port 4789 --jump ACCEPT") || !strings.Contains(postRules, "--in-interface "+OverlayInterfaceName(gateway.Name)) || !strings.Contains(postRules, "--source "+gateway.Spec.Overlay.CIDR) || !strings.Contains(postRules, "--destination-port 1053 --jump ACCEPT") {
		t.Fatalf("Gluetun forwarding adapter is not gateway-scoped: %s", postRules)
	}
	if !hasMount(engine, "engine-firewall") {
		t.Fatal("Gluetun does not receive its forwarding-policy adapter")
	}
}

func TestGluetunDelegatesProtonNATPMPToGatewayManager(t *testing.T) {
	gateway := testGateway()
	gateway.Spec.Engine.Type = "Gluetun"
	gateway.Spec.Provider.Name = "protonvpn"
	gateway.Spec.Provider.Protocol = "openvpn"
	gateway.Spec.PortForwarding = wayv1.PortForwardingSpec{Enabled: true, Driver: "ProtonNatPmp"}
	statefulSet := DesiredStatefulSet(gateway, WorkloadOptions{ManagerImage: digestImage("manager")})
	engine := statefulSet.Spec.Template.Spec.Containers[0]
	manager := statefulSet.Spec.Template.Spec.Containers[1]
	if !containsArgument(manager.Args, "--port-forward-driver=ProtonNatPmp") || !containsArgument(manager.Args, "--tunnel-interface="+TunnelInterface) {
		t.Fatalf("manager arguments = %#v", manager.Args)
	}
	environment := map[string]string{}
	for _, variable := range engine.Env {
		environment[variable.Name] = variable.Value
	}
	if environment["PORT_FORWARD_ONLY"] != "on" || environment["VPN_PORT_FORWARDING"] != "off" {
		t.Fatalf("port-forward ownership environment = %#v", environment)
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

func containsArgument(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if argument == wanted {
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
