// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	ManagerContainer = "waycloak-gateway-manager"
	EngineContainer  = "vpn-engine"
	VXLANPort        = 4789
	HealthPort       = 18080
	DNSPort          = 53
	TunnelInterface  = "wayvpn0"

	GatewayNameAnnotation = "internal.networking.waycloak.io/gateway-name"
	GatewayLabel          = "internal.networking.waycloak.io/gateway"
	EngineAuthKey         = "config.toml"
	EnginePostRulesKey    = "post-rules.txt"
	DesiredStateKey       = "gateway.json"
)

type WorkloadOptions struct {
	ManagerImage string
}

type Member struct {
	ID             string `json:"id"`
	OverlayAddress string `json:"overlayAddress"`
	UnderlayIP     string `json:"underlayIP"`
}

type DesiredState struct {
	GatewayName     string   `json:"gatewayName"`
	OverlayCIDR     string   `json:"overlayCIDR"`
	GatewayAddress  string   `json:"gatewayAddress"`
	VNI             int32    `json:"vni"`
	MTU             int32    `json:"mtu"`
	VXLANPort       int      `json:"vxlanPort"`
	TunnelInterface string   `json:"tunnelInterface"`
	Members         []Member `json:"members"`
}

func ResourceName(name string) string {
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:10]
	const prefix = "waycloak-gateway-"
	maxName := 63 - len(prefix) - len(sum) - 1
	if len(name) > maxName {
		name = name[:maxName]
	}
	return prefix + name + "-" + sum
}

func SelectorLabels(gateway *wayv1.VPNGateway) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "waycloak",
		"app.kubernetes.io/component": "gateway",
		GatewayLabel:                  ResourceName(gateway.Name),
	}
}

func DesiredService(gateway *wayv1.VPNGateway) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ResourceName(gateway.Name), Namespace: gateway.Namespace},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  SelectorLabels(gateway),
			Ports: []corev1.ServicePort{
				{Name: "vxlan", Port: VXLANPort, Protocol: corev1.ProtocolUDP, TargetPort: intstr.FromInt(VXLANPort)},
				{Name: "dns-udp", Port: DNSPort, Protocol: corev1.ProtocolUDP, TargetPort: intstr.FromInt(DNSPort)},
				{Name: "dns-tcp", Port: DNSPort, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt(DNSPort)},
				{Name: "health", Port: HealthPort, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt(HealthPort)},
			},
		},
	}
}

func DesiredConfigMap(gateway *wayv1.VPNGateway, members []Member) *corev1.ConfigMap {
	prefix, _ := netip.ParsePrefix(gateway.Spec.Overlay.CIDR)
	desired := DesiredState{GatewayName: gateway.Name, OverlayCIDR: gateway.Spec.Overlay.CIDR, GatewayAddress: prefix.Masked().Addr().Next().String(), VNI: gateway.Spec.Overlay.VNI, MTU: gateway.Spec.Overlay.MTU, VXLANPort: VXLANPort, TunnelInterface: TunnelInterface, Members: members}
	serialized, _ := json.Marshal(desired)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ResourceName(gateway.Name), Namespace: gateway.Namespace},
		Data: map[string]string{DesiredStateKey: string(serialized), EngineAuthKey: `[[roles]]
name = "waycloak-manager"
routes = ["GET /v1/dns/status", "GET /v1/publicip/ip"]
auth = "none"
`, EnginePostRulesKey: enginePostRules(gateway)},
	}
}

func DesiredStatefulSet(gateway *wayv1.VPNGateway, options WorkloadOptions) *appsv1.StatefulSet {
	one := int32(1)
	no := false
	yes := true
	root := int64(0)
	labels := SelectorLabels(gateway)
	annotations := map[string]string{GatewayNameAnnotation: gateway.Name}
	manager := corev1.Container{
		Name:            ManagerContainer,
		Image:           options.ManagerImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			"run",
			"--engine-type=" + gateway.Spec.Engine.Type,
			"--config-path=/run/waycloak/config/gateway.json",
			"--overlay-cidr=" + gateway.Spec.Overlay.CIDR,
			fmt.Sprintf("--vni=%d", gateway.Spec.Overlay.VNI),
			fmt.Sprintf("--mtu=%d", gateway.Spec.Overlay.MTU),
		},
		Ports:           []corev1.ContainerPort{{Name: "vxlan", ContainerPort: VXLANPort, Protocol: corev1.ProtocolUDP}, {Name: "health", ContainerPort: HealthPort, Protocol: corev1.ProtocolTCP}, {Name: "dns-udp", ContainerPort: DNSPort, Protocol: corev1.ProtocolUDP}, {Name: "dns-tcp", ContainerPort: DNSPort, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt(HealthPort)}}, PeriodSeconds: 2, TimeoutSeconds: 1, FailureThreshold: 1},
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, RunAsNonRoot: &no, RunAsUser: &root, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"NET_ADMIN", "NET_BIND_SERVICE"}}},
		VolumeMounts:    []corev1.VolumeMount{{Name: "runtime", MountPath: "/run/waycloak/runtime"}, {Name: "gateway-config", MountPath: "/run/waycloak/config", ReadOnly: true}},
	}
	engine := corev1.Container{
		Name:            EngineContainer,
		Image:           gateway.Spec.Engine.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, RunAsNonRoot: &no, RunAsUser: &root, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "NET_ADMIN", "SETGID", "SETUID"}}},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "credentials", MountPath: "/run/waycloak/credentials", ReadOnly: true},
			{Name: "engine-auth", MountPath: "/run/waycloak/engine-auth", ReadOnly: true},
			{Name: "engine-firewall", MountPath: "/iptables", ReadOnly: true},
			{Name: "tun", MountPath: "/dev/net/tun"},
			{Name: "engine-state", MountPath: "/gluetun"},
		},
	}
	if strings.EqualFold(gateway.Spec.Engine.Type, "Gluetun") {
		engine.Env = []corev1.EnvVar{
			{Name: "VPN_SERVICE_PROVIDER", Value: gateway.Spec.Provider.Name},
			{Name: "VPN_TYPE", Value: strings.ToLower(gateway.Spec.Provider.Protocol)},
			{Name: "SERVER_COUNTRIES", Value: gateway.Spec.Provider.Region},
			{Name: "OPENVPN_USER_SECRETFILE", Value: "/run/waycloak/credentials/username"},
			{Name: "OPENVPN_PASSWORD_SECRETFILE", Value: "/run/waycloak/credentials/password"},
			{Name: "HEALTH_SERVER_ADDRESS", Value: "127.0.0.1:9999"},
			{Name: "HTTP_CONTROL_SERVER_ADDRESS", Value: "127.0.0.1:8000"},
			{Name: "HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", Value: "/run/waycloak/engine-auth/config.toml"},
			{Name: "PUBLICIP_ENABLED", Value: "on"},
			{Name: "VPN_INTERFACE", Value: TunnelInterface},
			{Name: "FIREWALL_INPUT_PORTS", Value: fmt.Sprint(HealthPort)},
		}
	}
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: ResourceName(gateway.Name), Namespace: gateway.Namespace},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         ResourceName(gateway.Name),
			Replicas:            &one,
			PodManagementPolicy: appsv1.OrderedReadyPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &no,
					SecurityContext:              &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					Containers:                   []corev1.Container{engine, manager},
					Volumes: []corev1.Volume{
						{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: gateway.Spec.Provider.CredentialsSecretRef.Name}}},
						{Name: "engine-auth", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ResourceName(gateway.Name)}}}},
						{Name: "engine-firewall", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ResourceName(gateway.Name)}, Items: []corev1.KeyToPath{{Key: EnginePostRulesKey, Path: EnginePostRulesKey}}}}},
						{Name: "gateway-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ResourceName(gateway.Name)}, Items: []corev1.KeyToPath{{Key: DesiredStateKey, Path: DesiredStateKey}}}}},
						{Name: "tun", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev/net/tun", Type: hostPathType(corev1.HostPathCharDev)}}},
						{Name: "engine-state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "runtime", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
}

func enginePostRules(gateway *wayv1.VPNGateway) string {
	overlay := OverlayInterfaceName(gateway.Name)
	return fmt.Sprintf("iptables --policy FORWARD ACCEPT\niptables --append INPUT --in-interface %s --source %s --protocol udp --destination-port %d --jump ACCEPT\niptables --append INPUT --in-interface %s --source %s --protocol tcp --destination-port %d --jump ACCEPT\niptables --append INPUT --in-interface %s --source %s --protocol tcp --destination-port %d --jump ACCEPT\n", overlay, gateway.Spec.Overlay.CIDR, DNSPort, overlay, gateway.Spec.Overlay.CIDR, DNSPort, overlay, gateway.Spec.Overlay.CIDR, HealthPort)
}

func hostPathType(value corev1.HostPathType) *corev1.HostPathType { return &value }
