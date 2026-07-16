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

func TestDesiredStatefulSetUsesNativeGluetunConfigurationWithoutReadingSecrets(t *testing.T) {
	gateway := testGateway()
	gateway.Spec.Engine.Type = "Gluetun"
	gateway.Spec.Provider = nil
	gateway.Spec.Engine.Config = &wayv1.EngineNativeConfigSpec{
		EnvFrom: []corev1.LocalObjectReference{{Name: "mullvad-wireguard"}},
		Files: []wayv1.EngineFileSource{
			{SecretRef: &corev1.LocalObjectReference{Name: "wireguard-config"}, MountPath: "/gluetun/wireguard"},
			{ConfigMapRef: &corev1.LocalObjectReference{Name: "custom-openvpn"}, MountPath: "/run/engine-native/openvpn"},
		},
	}
	statefulSet := DesiredStatefulSet(gateway, WorkloadOptions{ManagerImage: digestImage("manager"), EngineConfigDigest: "sha256:native"})
	engine := statefulSet.Spec.Template.Spec.Containers[0]
	manager := statefulSet.Spec.Template.Spec.Containers[1]
	renderer := statefulSet.Spec.Template.Spec.InitContainers[0]
	if len(engine.EnvFrom) != 1 || engine.EnvFrom[0].ConfigMapRef == nil || engine.EnvFrom[0].ConfigMapRef.Name != "mullvad-wireguard" {
		t.Fatalf("native env sources = %#v", engine.EnvFrom)
	}
	if hasEnvironment(engine.Env, "VPN_SERVICE_PROVIDER") || hasEnvironment(engine.Env, "VPN_TYPE") {
		t.Fatalf("native values were copied into the Pod environment: %#v", engine.Env)
	}
	if !hasMount(engine, "engine-native-file-0") || !hasMount(engine, "engine-native-file-1") || hasMount(manager, "engine-native-file-0") || hasMount(manager, "engine-native-file-1") || hasMount(renderer, "engine-native-file-0") || hasMount(renderer, "engine-native-file-1") {
		t.Fatalf("native file isolation engine=%#v manager=%#v renderer=%#v", engine.VolumeMounts, manager.VolumeMounts, renderer.VolumeMounts)
	}
	if hasMount(engine, "credentials") || hasMount(manager, "credentials") {
		t.Fatal("native configuration unexpectedly created the legacy credential mount")
	}
	if statefulSet.Spec.Template.Annotations[EngineConfigDigestAnnotation] != "sha256:native" {
		t.Fatalf("engine config digest annotation = %#v", statefulSet.Spec.Template.Annotations)
	}
	volumes := make(map[string]corev1.Volume)
	for _, volume := range statefulSet.Spec.Template.Spec.Volumes {
		volumes[volume.Name] = volume
	}
	if volumes["engine-native-file-0"].Secret == nil || volumes["engine-native-file-0"].Secret.SecretName != "wireguard-config" {
		t.Fatalf("wireguard Secret volume = %#v", volumes["engine-native-file-0"])
	}
	if volumes["engine-native-file-1"].ConfigMap == nil || volumes["engine-native-file-1"].ConfigMap.Name != "custom-openvpn" {
		t.Fatalf("custom OpenVPN ConfigMap volume = %#v", volumes["engine-native-file-1"])
	}
	for _, reserved := range []string{"DNS_ADDRESS", "FIREWALL_INPUT_PORTS", "HEALTH_SERVER_ADDRESS", "HTTP_CONTROL_SERVER_ADDRESS", "HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", "PUBLICIP_ENABLED", "VPN_INTERFACE", "VPN_PORT_FORWARDING"} {
		if !hasEnvironment(engine.Env, reserved) {
			t.Fatalf("native engine is missing reserved setting %s: %#v", reserved, engine.Env)
		}
	}
}

func TestDesiredStatefulSetDoesNotCreateUnconsumedNativeVolumesForOtherEngines(t *testing.T) {
	gateway := testGateway()
	gateway.Spec.Engine.Type = "Test"
	gateway.Spec.Provider = nil
	gateway.Spec.Engine.Config = &wayv1.EngineNativeConfigSpec{
		EnvFrom: []corev1.LocalObjectReference{{Name: "native"}},
		Files:   []wayv1.EngineFileSource{{SecretRef: &corev1.LocalObjectReference{Name: "native-secret"}, MountPath: "/run/engine-native/credentials"}},
	}
	statefulSet := DesiredStatefulSet(gateway, WorkloadOptions{ManagerImage: digestImage("manager")})
	engine := statefulSet.Spec.Template.Spec.Containers[0]
	if len(engine.EnvFrom) != 0 || hasMount(engine, "engine-native-file-0") {
		t.Fatalf("non-Gluetun engine consumed native projection: envFrom=%#v mounts=%#v", engine.EnvFrom, engine.VolumeMounts)
	}
	for _, volume := range statefulSet.Spec.Template.Spec.Volumes {
		if volume.Name == "engine-native-file-0" {
			t.Fatalf("non-Gluetun engine received an unmounted native volume: %#v", volume)
		}
	}
}

func TestMembershipGenerationIsDeterministicAndChangesOnlyWithMembership(t *testing.T) {
	members := []Member{{ID: "b", OverlayAddress: "172.30.99.3", UnderlayIP: "10.42.0.3"}, {ID: "a", OverlayAddress: "172.30.99.2", UnderlayIP: "10.42.0.2"}}
	first := MembershipGeneration(members)
	if first != MembershipGeneration([]Member{members[1], members[0]}) {
		t.Fatal("membership generation depends on input ordering")
	}
	replaced := append([]Member(nil), members...)
	replaced[0].UnderlayIP = "10.42.1.3"
	if first == MembershipGeneration(replaced) {
		t.Fatal("underlay replacement did not advance membership generation")
	}
	gateway := testGateway()
	configMap := DesiredConfigMap(gateway, members)
	if configMap.Data[DesiredMembershipGenerationKey] != first || !strings.Contains(configMap.Data[DesiredStateKey], first) {
		t.Fatalf("published membership generation = %#v", configMap.Data)
	}
}

func TestResourceNameIsStableAndBounded(t *testing.T) {
	name := "gateway-with-a-deliberately-long-name-that-needs-to-be-shortened-for-owned-resources"
	first := ResourceName(name)
	if len(first) > 63 || first != ResourceName(name) || first == ResourceName(name+"-different") {
		t.Fatalf("resource name = %q", first)
	}
}

func TestDesiredStatefulSetReservesGeneratedNameSuffixes(t *testing.T) {
	gateway := testGateway()
	gateway.Name = "waycloak-real-pf-18c20e8323561f28-gateway-with-a-deliberately-long-name"

	statefulSet := DesiredStatefulSet(gateway, WorkloadOptions{ManagerImage: digestImage("manager")})
	if got := len(statefulSet.Name); got > statefulSetNameMaxLength {
		t.Fatalf("StatefulSet name length = %d, want at most %d: %q", got, statefulSetNameMaxLength, statefulSet.Name)
	}
	if got := len(statefulSet.Name + "-0"); got > 63 {
		t.Fatalf("derived Pod name length = %d, want at most 63", got)
	}
	if got := len(statefulSet.Name + "-0123456789"); got > 63 {
		t.Fatalf("derived controller revision label length = %d, want at most 63", got)
	}
	if statefulSet.Name != statefulSetResourceName(gateway.Name) {
		t.Fatalf("StatefulSet name = %q, want deterministic name %q", statefulSet.Name, statefulSetResourceName(gateway.Name))
	}
	if statefulSet.Name == statefulSetResourceName(gateway.Name+"-different") {
		t.Fatalf("distinct gateway names collided at %q", statefulSet.Name)
	}
	if statefulSet.Spec.ServiceName != ResourceName(gateway.Name) {
		t.Fatalf("service name = %q, want %q", statefulSet.Spec.ServiceName, ResourceName(gateway.Name))
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
	if !strings.Contains(postRules, "iptables --policy FORWARD ACCEPT") || !strings.Contains(postRules, "iptables --append INPUT --protocol udp --destination-port 4789 --jump ACCEPT") || !strings.Contains(postRules, "iptables --append OUTPUT --protocol udp --destination-port 4789 --jump ACCEPT") || !strings.Contains(postRules, "--in-interface "+OverlayInterfaceName(gateway.Name)) || !strings.Contains(postRules, "--source "+gateway.Spec.Overlay.CIDR) || !strings.Contains(postRules, "--destination-port 1053 --jump ACCEPT") {
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

func hasEnvironment(environment []corev1.EnvVar, name string) bool {
	for _, variable := range environment {
		if variable.Name == name {
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
			Provider: &wayv1.ProviderSpec{Name: "test", CredentialsSecretRef: corev1.LocalObjectReference{Name: "vpn-credentials"}},
			Overlay:  wayv1.OverlaySpec{CIDR: "172.30.99.0/24", VNI: 7999, MTU: 1320},
		},
	}
}

func digestImage(name string) string {
	return "registry.invalid/" + name + "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
