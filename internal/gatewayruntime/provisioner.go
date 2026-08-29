// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewayruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"github.com/Amoenus/waycloak/internal/gatewaydataplane"
	"github.com/Amoenus/waycloak/internal/portforward"
	"github.com/Amoenus/waycloak/internal/provider/gluetun"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	managedLabel                    = "runtime.networking.waycloak.io/gateway-uid"
	releaseVersionAnnotation        = "runtime.networking.waycloak.io/release-version"
	releaseManifestDigestAnnotation = "runtime.networking.waycloak.io/release-manifest-digest"
	staticControlAuthConfigPath     = "/etc/waycloak/gluetun-control-auth.toml"
	generatedControlAuthDirectory   = "/var/run/waycloak-gluetun-control"
	generatedControlAuthConfigPath  = generatedControlAuthDirectory + "/auth.toml"
	generatedControlAPIKeyPath      = generatedControlAuthDirectory + "/api-key"
	portForwardTLSMountPath         = "/var/run/secrets/waycloak-port-forward-runtime"
	coreDNSConfigPath               = "/Corefile"
	healthObservationAttempts       = 2
	healthObservationAttemptTimeout = 2 * time.Second
	healthObservationRetryDelay     = 25 * time.Millisecond
)

var defaultHealthHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: healthObservationAttemptTimeout, Transport: transport}
}()

type HealthObservationEvent struct {
	State          string
	Phase          string
	Class          string
	Attempts       int
	Latency        time.Duration
	UnavailableFor time.Duration
}

type healthObservationFailure struct {
	phase   string
	class   string
	started time.Time
}

type healthObservationResult struct {
	attempts         int
	latency          time.Duration
	lastFailurePhase string
	lastFailureClass string
}

type healthObservationError struct {
	phase    string
	class    string
	attempts int
	latency  time.Duration
}

func (failure healthObservationError) Error() string {
	return fmt.Sprintf("gateway health observation failed phase=%s class=%s attempts=%d latency=%s", failure.phase, failure.class, failure.attempts, failure.latency.Round(time.Millisecond))
}

type Provisioner struct {
	Client                  client.Client
	Reader                  client.Reader
	EngineImage             string
	AgentImage              string
	CoreDNSImage            string
	PortForwardRuntimeImage string
	PortForwardRuntimePort  uint16
	AdapterPort             uint16
	AdapterEnabled          bool
	ReleaseIdentity         wayv1.ReleaseIdentity
	OverlayCIDR             netip.Prefix
	VNI                     uint32
	MTU                     int32
	VXLANPort               uint16
	HealthPort              uint16
	ClusterDNSUpstream      netip.AddrPort
	ClusterDomain           string
	HTTPClient              *http.Client
	HealthObservationHook   func(HealthObservationEvent)
	healthObservationMu     sync.Mutex
	healthFailures          map[string]healthObservationFailure
}

func (provisioner *Provisioner) Reconcile(ctx context.Context, gateway *wayv1.VPNGateway) (waycontroller.GatewayRuntimeObservation, error) {
	if err := provisioner.validate(gateway); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	configName, secretName, runtimeTLSSecretName, err := inputNames(gateway)
	if err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	configMap := &corev1.ConfigMap{}
	if err := provisioner.reader().Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: configName}, configMap); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	if err := validateEngineConfig(configMap.Data); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	portForwardCapability := ""
	if requestsFeature(gateway, wayv1.FeaturePortForwardSingleActive) {
		portForwardCapability, err = gluetun.PortForwardCapabilityForConfig(configMap.Data)
		if err != nil {
			return waycontroller.GatewayRuntimeObservation{}, err
		}
	}
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
	if err := provisioner.reader().Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: secretName}, secret); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	var runtimeTLSSecret *metav1.PartialObjectMetadata
	if runtimeTLSSecretName != "" {
		runtimeTLSSecret = &metav1.PartialObjectMetadata{}
		runtimeTLSSecret.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
		if err := provisioner.reader().Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: runtimeTLSSecretName}, runtimeTLSSecret); err != nil {
			return waycontroller.GatewayRuntimeObservation{}, err
		}
	}
	name := runtimeName(gateway.Name)
	labels := map[string]string{"app.kubernetes.io/name": "waycloak-gateway", "app.kubernetes.io/component": "gateway", "app.kubernetes.io/instance": name, managedLabel: shortHash(string(gateway.UID))}
	service := provisioner.desiredService(gateway, name, labels)
	if err := provisioner.reconcileObject(ctx, service); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	extended := requestsFeature(gateway, wayv1.FeaturePortForwardSingleActive)
	if err := provisioner.reconcilePortForwardService(ctx, gateway, labels, extended); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	dnsConfig := provisioner.desiredCoreDNSConfig(gateway, name, labels)
	if err := provisioner.reconcileObject(ctx, dnsConfig); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	statefulSet := provisioner.desiredStatefulSet(gateway, name, labels, configName, secretName, runtimeTLSSecretName, portForwardCapability, configMap, secret, runtimeTLSSecret, dnsConfig)
	if err := provisioner.reconcileObject(ctx, statefulSet); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	observation := waycontroller.GatewayRuntimeObservation{Programmed: true, Addresses: provisioner.addresses("")}
	pods := &corev1.PodList{}
	if err := provisioner.reader().List(ctx, pods, client.InNamespace(gateway.Namespace), client.MatchingLabels{managedLabel: shortHash(string(gateway.UID))}); err != nil {
		return observation, err
	}
	current := currentPod(pods.Items, statefulSet)
	if current == nil || current.Status.PodIP == "" || current.Status.Phase != corev1.PodRunning {
		return observation, nil
	}
	endpoint, err := netip.ParseAddr(current.Status.PodIP)
	if err != nil || !endpoint.Is4() {
		return observation, nil
	}
	observation.Addresses = provisioner.addresses(netip.AddrPortFrom(endpoint, provisioner.VXLANPort).String())
	health, healthResult, healthErr := provisioner.observeHealth(ctx, "http://"+netip.AddrPortFrom(endpoint, provisioner.HealthPort).String()+"/v1/status")
	if healthErr != nil {
		provisioner.reportHealthObservationFailure(ctx, gateway, healthErr)
		return observation, nil
	}
	provisioner.reportHealthObservationRecovery(ctx, gateway, healthResult)
	observation.Ready = health.Ready
	observation.TunnelReady = health.TunnelReady
	observation.DNSReady = health.DNSReady
	observation.MembershipApplied = health.Ready
	return observation, nil
}

func (provisioner *Provisioner) validate(gateway *wayv1.VPNGateway) error {
	if provisioner.Client == nil || gateway == nil || gateway.UID == "" || !ValidExactImage(provisioner.EngineImage) || !ValidExactImage(provisioner.AgentImage) || !ValidExactImage(provisioner.CoreDNSImage) || !waycontroller.ValidReleaseIdentity(provisioner.ReleaseIdentity) || !provisioner.OverlayCIDR.IsValid() || !provisioner.OverlayCIDR.Addr().Is4() || provisioner.OverlayCIDR.Bits() < 16 || provisioner.OverlayCIDR.Bits() > 29 || !provisioner.ClusterDNSUpstream.IsValid() || !provisioner.ClusterDNSUpstream.Addr().Is4() || provisioner.ClusterDNSUpstream.Addr().IsLoopback() || provisioner.ClusterDNSUpstream.Port() != 53 || len(utilvalidation.IsDNS1123Subdomain(provisioner.ClusterDomain)) != 0 || provisioner.VNI == 0 || provisioner.VNI > 16777215 || provisioner.MTU < 576 || provisioner.MTU > 9000 || provisioner.VXLANPort == 0 || provisioner.HealthPort == 0 {
		return errors.New("exact gateway runtime release, images, and reviewed network parameters are required")
	}
	runtimeConfigured := provisioner.PortForwardRuntimeImage != "" || provisioner.PortForwardRuntimePort != 0 || provisioner.AdapterEnabled
	if runtimeConfigured && (!ValidExactImage(provisioner.PortForwardRuntimeImage) || provisioner.PortForwardRuntimePort == 0 || provisioner.AdapterEnabled && provisioner.AdapterPort == 0) {
		return errors.New("complete exact port-forward gateway runtime configuration is required")
	}
	if requestsFeature(gateway, wayv1.FeaturePortForwardSingleActive) && !runtimeConfigured {
		return errors.New("SingleActive port forwarding runtime is unavailable")
	}
	if requestsFeature(gateway, wayv1.FeatureWorkloadAdapter) && (!requestsFeature(gateway, wayv1.FeaturePortForwardSingleActive) || !provisioner.AdapterEnabled) {
		return errors.New("WorkloadAdapter runtime is unavailable")
	}
	return nil
}
func inputNames(gateway *wayv1.VPNGateway) (string, string, string, error) {
	var config, secret, runtimeTLS string
	for _, ref := range gateway.Spec.NativeConfigRefs {
		if ref.Role == waycontroller.GluetunEnvironmentRole {
			if config != "" {
				return "", "", "", errors.New("multiple Gluetun environment references are unsupported")
			}
			config = string(ref.Name)
		}
	}
	for _, ref := range gateway.Spec.CredentialRefs {
		switch ref.Role {
		case waycontroller.OpenVPNCredentialsRole:
			if secret != "" {
				return "", "", "", errors.New("multiple OpenVPN credential references are unsupported")
			}
			secret = string(ref.Name)
		case waycontroller.GatewayRuntimeTLSRole:
			if runtimeTLS != "" {
				return "", "", "", errors.New("multiple gateway runtime TLS references are unsupported")
			}
			runtimeTLS = string(ref.Name)
		}
	}
	if config == "" || secret == "" {
		return "", "", "", errors.New("one native configuration and credential reference are required")
	}
	if requestsFeature(gateway, wayv1.FeaturePortForwardSingleActive) && runtimeTLS == "" {
		return "", "", "", errors.New("one gateway runtime TLS reference is required")
	}
	if !requestsFeature(gateway, wayv1.FeaturePortForwardSingleActive) && runtimeTLS != "" {
		return "", "", "", errors.New("gateway runtime TLS reference requires SingleActive port forwarding")
	}
	return config, secret, runtimeTLS, nil
}

func validateEngineConfig(data map[string]string) error {
	for _, key := range []string{"HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", "HTTP_CONTROL_SERVER_AUTH_DEFAULT_ROLE", "VPN_PORT_FORWARDING", "VPN_PORT_FORWARDING_PROVIDER", "VPN_PORT_FORWARDING_STATUS_FILE", "VPN_PORT_FORWARDING_UP_COMMAND", "VPN_PORT_FORWARDING_DOWN_COMMAND", "VPN_PORT_FORWARDING_LISTENING_PORT"} {
		if _, exists := data[key]; exists {
			return errors.New("gluetun control authentication is release-owned")
		}
	}
	return nil
}

func (provisioner *Provisioner) desiredService(gateway *wayv1.VPNGateway, name string, labels map[string]string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateway.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner(gateway)}}, Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Selector: labels, Ports: []corev1.ServicePort{{Name: "health", Port: int32(provisioner.HealthPort), Protocol: corev1.ProtocolTCP}}}}
}

func (provisioner *Provisioner) desiredCoreDNSConfig(gateway *wayv1.VPNGateway, name string, labels map[string]string) *corev1.ConfigMap {
	listenAddress := gatewayAddress(provisioner.OverlayCIDR).String()
	corefile := fmt.Sprintf(`%s:%d {
    bind %s
    errors
    forward . %s {
        policy sequential
        max_concurrent 128
        expire 10s
        health_check 1s
        failfast_all_unhealthy_upstreams
    }
}
.:%d {
    bind %s
    errors
    forward . 127.0.0.1:53 {
        policy sequential
        max_concurrent 128
        expire 10s
        health_check 1s
        failfast_all_unhealthy_upstreams
    }
}
`, provisioner.ClusterDomain, gatewaydataplane.DNSListenPort, listenAddress, provisioner.ClusterDNSUpstream.String(), gatewaydataplane.DNSListenPort, listenAddress)
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-dns", Namespace: gateway.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner(gateway)}}, Data: map[string]string{"Corefile": corefile}}
}

func (provisioner *Provisioner) reconcilePortForwardService(ctx context.Context, gateway *wayv1.VPNGateway, labels map[string]string, enabled bool) error {
	name := portforward.GatewayRuntimeServiceName(gateway.Namespace, gateway.Name)
	if !enabled {
		current := &corev1.Service{}
		err := provisioner.Client.Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: name}, current)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !ownedByGateway(current.OwnerReferences, gateway) {
			return errors.New("gateway runtime Service name is owned by another object")
		}
		uid := current.UID
		return provisioner.Client.Delete(ctx, current, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateway.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner(gateway)}}, Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Name: "runtime", Port: int32(provisioner.PortForwardRuntimePort), Protocol: corev1.ProtocolTCP}}}}
	return provisioner.reconcileObject(ctx, service)
}

func (provisioner *Provisioner) desiredStatefulSet(gateway *wayv1.VPNGateway, name string, labels map[string]string, configName, secretName, runtimeTLSSecretName, portForwardCapability string, configMap *corev1.ConfigMap, secret, runtimeTLSSecret *metav1.PartialObjectMetadata, dnsConfig *corev1.ConfigMap) *appsv1.StatefulSet {
	replicas := int32(1)
	no := false
	yes := true
	runAsRoot := int64(0)
	runAsNonRoot := true
	coreDNSUser := int64(65532)
	runtimeTLSResourceVersion := ""
	if runtimeTLSSecret != nil {
		runtimeTLSResourceVersion = runtimeTLSSecret.GetResourceVersion()
	}
	hash := sha256.Sum256([]byte(configMap.ResourceVersion + "\x00" + secret.GetResourceVersion() + "\x00" + runtimeTLSResourceVersion + "\x00" + dnsConfig.Data["Corefile"]))
	engineControlPath := staticControlAuthConfigPath
	engineEnvironment := []corev1.EnvVar{{Name: "OPENVPN_USER", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "username"}}}, {Name: "OPENVPN_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "password"}}}, {Name: "VPN_PORT_FORWARDING", Value: "off"}}
	engineVolumeMounts := []corev1.VolumeMount{{Name: "tun", MountPath: "/dev/net/tun"}, {Name: "engine-state", MountPath: "/gluetun"}}
	agentArgs := []string{"--gateway-uid=" + string(gateway.UID), "--overlay-cidr=" + provisioner.OverlayCIDR.Masked().String(), "--gateway-address=" + gatewayAddress(provisioner.OverlayCIDR).String(), "--cluster-dns-upstream=" + provisioner.ClusterDNSUpstream.String(), "--cluster-domain=" + provisioner.ClusterDomain, "--vxlan-port=" + strconv.Itoa(int(provisioner.VXLANPort)), "--health-port=" + strconv.Itoa(int(provisioner.HealthPort)), "--vni=" + strconv.FormatUint(uint64(provisioner.VNI), 10), "--mtu=" + strconv.Itoa(int(provisioner.MTU))}
	agentVolumeMounts := []corev1.VolumeMount{}
	initContainers := []corev1.Container{{Name: "gateway-dataplane-init", Image: provisioner.AgentImage, ImagePullPolicy: corev1.PullIfNotPresent, Args: append(append([]string{}, agentArgs...), "--initialize-dataplane"), SecurityContext: &corev1.SecurityContext{RunAsUser: &runAsRoot, RunAsGroup: &runAsRoot, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}, Drop: []corev1.Capability{"ALL"}}}}}
	volumes := []corev1.Volume{{Name: "tun", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev/net/tun", Type: pointer(corev1.HostPathCharDev)}}}, {Name: "engine-state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "coredns-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: dnsConfig.Name}, DefaultMode: pointer(int32(0o444))}}}}
	if runtimeTLSSecretName != "" {
		engineControlPath = generatedControlAuthConfigPath
		engineEnvironment[2].Value = "on"
		engineEnvironment = append(engineEnvironment, corev1.EnvVar{Name: "VPN_PORT_FORWARDING_STATUS_FILE", Value: "/gluetun/forwarded_port"}, corev1.EnvVar{Name: "VPN_PORT_FORWARDING_UP_COMMAND", Value: ""}, corev1.EnvVar{Name: "VPN_PORT_FORWARDING_DOWN_COMMAND", Value: ""}, corev1.EnvVar{Name: "VPN_PORT_FORWARDING_LISTENING_PORT", Value: "0"})
		controlMount := corev1.VolumeMount{Name: "gluetun-control-auth", MountPath: generatedControlAuthDirectory, ReadOnly: true}
		engineVolumeMounts = append(engineVolumeMounts, controlMount)
		agentVolumeMounts = append(agentVolumeMounts, controlMount)
		agentArgs = append(agentArgs, "--gluetun-control-api-key-file="+generatedControlAPIKeyPath)
		initContainers = append(initContainers, corev1.Container{Name: "gluetun-control-auth", Image: provisioner.PortForwardRuntimeImage, ImagePullPolicy: corev1.PullIfNotPresent,
			Args:            []string{"--write-gluetun-control-auth=" + generatedControlAuthConfigPath, "--gluetun-control-identity-source=" + portForwardTLSMountPath + "/tls.key", "--gluetun-control-api-key-output=" + generatedControlAPIKeyPath},
			SecurityContext: &corev1.SecurityContext{RunAsUser: &runAsRoot, RunAsGroup: &runAsRoot, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts:    []corev1.VolumeMount{{Name: "port-forward-runtime-tls", MountPath: portForwardTLSMountPath, ReadOnly: true}, {Name: "gluetun-control-auth", MountPath: generatedControlAuthDirectory}}})
		volumes = append(volumes, corev1.Volume{Name: "gluetun-control-auth", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}})
	}
	engineEnvironment = append(engineEnvironment, corev1.EnvVar{Name: "HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", Value: engineControlPath})
	containers := []corev1.Container{
		{Name: "vpn-engine", Image: provisioner.EngineImage, ImagePullPolicy: corev1.PullIfNotPresent, EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}}, Env: engineEnvironment, SecurityContext: &corev1.SecurityContext{RunAsUser: &runAsRoot, RunAsGroup: &runAsRoot, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &no, Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "CHOWN", "DAC_OVERRIDE", "SETUID", "KILL"}, Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: engineVolumeMounts},
		{Name: "coredns", Image: provisioner.CoreDNSImage, ImagePullPolicy: corev1.PullIfNotPresent, Args: []string{"-conf", coreDNSConfigPath}, Ports: []corev1.ContainerPort{{Name: "dns-udp", ContainerPort: int32(gatewaydataplane.DNSListenPort), Protocol: corev1.ProtocolUDP}, {Name: "dns-tcp", ContainerPort: int32(gatewaydataplane.DNSListenPort), Protocol: corev1.ProtocolTCP}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m"), corev1.ResourceMemory: resource.MustParse("16Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}, SecurityContext: &corev1.SecurityContext{RunAsUser: &coreDNSUser, RunAsGroup: &coreDNSUser, RunAsNonRoot: &runAsNonRoot, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "coredns-config", MountPath: coreDNSConfigPath, SubPath: "Corefile", ReadOnly: true}}},
		{Name: "gateway-agent", Image: provisioner.AgentImage, ImagePullPolicy: corev1.PullIfNotPresent, Args: agentArgs, Ports: []corev1.ContainerPort{{Name: "vxlan", ContainerPort: int32(provisioner.VXLANPort), Protocol: corev1.ProtocolUDP}, {Name: "health", ContainerPort: int32(provisioner.HealthPort), Protocol: corev1.ProtocolTCP}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstrFromInt(int(provisioner.HealthPort))}}, PeriodSeconds: 2, FailureThreshold: 2}, SecurityContext: &corev1.SecurityContext{RunAsUser: &runAsRoot, RunAsGroup: &runAsRoot, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}, Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: agentVolumeMounts},
	}
	if runtimeTLSSecretName != "" {
		args := []string{"--gateway-uid=" + string(gateway.UID), "--listen-address=:" + strconv.Itoa(int(provisioner.PortForwardRuntimePort)), "--tls-cert=" + portForwardTLSMountPath + "/tls.crt", "--tls-key=" + portForwardTLSMountPath + "/tls.key", "--client-ca=" + portForwardTLSMountPath + "/ca.crt", "--engine-port-forward-capability=" + portForwardCapability, "--gluetun-control-api-key-file=" + generatedControlAPIKeyPath}
		if requestsFeature(gateway, wayv1.FeatureWorkloadAdapter) {
			args = append(args, "--adapter-ca="+portForwardTLSMountPath+"/adapter-ca.crt", "--adapter-client-cert="+portForwardTLSMountPath+"/adapter-client.crt", "--adapter-client-key="+portForwardTLSMountPath+"/adapter-client.key", "--adapter-port="+strconv.Itoa(int(provisioner.AdapterPort)), "--cluster-domain="+provisioner.ClusterDomain)
		}
		containers = append(containers, corev1.Container{Name: "port-forward-runtime", Image: provisioner.PortForwardRuntimeImage, ImagePullPolicy: corev1.PullIfNotPresent, Args: args,
			Ports:           []corev1.ContainerPort{{Name: "runtime", ContainerPort: int32(provisioner.PortForwardRuntimePort), Protocol: corev1.ProtocolTCP}},
			ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstrFromInt(int(provisioner.PortForwardRuntimePort))}}, PeriodSeconds: 2, FailureThreshold: 2},
			SecurityContext: &corev1.SecurityContext{RunAsUser: &runAsRoot, RunAsGroup: &runAsRoot, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}, Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts:    []corev1.VolumeMount{{Name: "port-forward-runtime-tls", MountPath: portForwardTLSMountPath, ReadOnly: true}, {Name: "gluetun-control-auth", MountPath: generatedControlAuthDirectory, ReadOnly: true}}})
		volumes = append(volumes, corev1.Volume{Name: "port-forward-runtime-tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: runtimeTLSSecretName, DefaultMode: pointer(int32(0o400))}}})
	}
	// The StatefulSet is a singleton rollout-control primitive, not durable
	// application storage. Its engine state is EmptyDir-backed, and OnDelete
	// keeps a target template inert until an operator activates the reviewed
	// fail-closed gateway replacement.
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateway.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner(gateway)}}, Spec: appsv1.StatefulSetSpec{ServiceName: name, Replicas: &replicas, UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{"runtime.networking.waycloak.io/input-revision": hex.EncodeToString(hash[:]), releaseVersionAnnotation: provisioner.ReleaseIdentity.Version, releaseManifestDigestAnnotation: provisioner.ReleaseIdentity.ManifestDigest}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, TerminationGracePeriodSeconds: pointer(int64(20)), NodeSelector: gateway.Spec.Placement.NodeSelector, Tolerations: gateway.Spec.Placement.Tolerations, DNSConfig: &corev1.PodDNSConfig{Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: pointer("1")}}}, InitContainers: initContainers, Containers: containers, Volumes: volumes}}}}
}

func (provisioner *Provisioner) reconcileObject(ctx context.Context, object client.Object) error {
	current := object.DeepCopyObject().(client.Object)
	err := provisioner.Client.Get(ctx, client.ObjectKeyFromObject(object), current)
	if apierrors.IsNotFound(err) {
		return provisioner.Client.Create(ctx, object, client.FieldOwner("networking.waycloak.io/gateway-runtime"))
	}
	if err != nil {
		return err
	}
	object.SetUID(current.GetUID())
	switch desired := object.(type) {
	case *corev1.Service:
		existing := current.(*corev1.Service)
		updated := existing.DeepCopy()
		updated.Labels, updated.OwnerReferences, updated.Spec.Selector, updated.Spec.Ports = desired.Labels, desired.OwnerReferences, desired.Spec.Selector, desired.Spec.Ports
		if reflect.DeepEqual(existing, updated) {
			return nil
		}
		return provisioner.Client.Patch(ctx, updated, client.MergeFrom(existing), client.FieldOwner("networking.waycloak.io/gateway-runtime"))
	case *corev1.ConfigMap:
		existing := current.(*corev1.ConfigMap)
		updated := existing.DeepCopy()
		updated.Labels, updated.OwnerReferences, updated.Data = desired.Labels, desired.OwnerReferences, desired.Data
		if reflect.DeepEqual(existing, updated) {
			return nil
		}
		return provisioner.Client.Patch(ctx, updated, client.MergeFrom(existing), client.FieldOwner("networking.waycloak.io/gateway-runtime"))
	case *appsv1.StatefulSet:
		existing := current.(*appsv1.StatefulSet)
		updated := existing.DeepCopy()
		updated.Labels, updated.OwnerReferences = desired.Labels, desired.OwnerReferences
		updated.Spec.ServiceName, updated.Spec.Replicas, updated.Spec.UpdateStrategy, updated.Spec.Selector, updated.Spec.Template = desired.Spec.ServiceName, desired.Spec.Replicas, desired.Spec.UpdateStrategy, desired.Spec.Selector, desired.Spec.Template
		if reflect.DeepEqual(existing, updated) {
			return nil
		}
		return provisioner.Client.Patch(ctx, updated, client.MergeFrom(existing), client.FieldOwner("networking.waycloak.io/gateway-runtime"))
	default:
		return errors.New("unsupported gateway runtime object")
	}
}
func (provisioner *Provisioner) addresses(endpoint string) []wayv1.GatewayAddress {
	values := []wayv1.GatewayAddress{{Type: wayv1.GatewayAddressTypeOverlayCIDR, Value: provisioner.OverlayCIDR.Masked().String()}, {Type: wayv1.GatewayAddressTypeOverlayAddress, Value: gatewayAddress(provisioner.OverlayCIDR).String()}, {Type: wayv1.GatewayAddressTypeOverlayHealthPort, Value: strconv.Itoa(int(provisioner.HealthPort))}, {Type: wayv1.GatewayAddressTypeVNI, Value: strconv.FormatUint(uint64(provisioner.VNI), 10)}, {Type: wayv1.GatewayAddressTypeMTU, Value: strconv.Itoa(int(provisioner.MTU))}}
	if endpoint != "" {
		values = append(values, wayv1.GatewayAddress{Type: wayv1.GatewayAddressTypeUnderlayEndpoint, Value: endpoint})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Type < values[j].Type })
	return values
}
func currentPod(pods []corev1.Pod, statefulSet *appsv1.StatefulSet) *corev1.Pod {
	for index := range pods {
		pod := &pods[index]
		for _, reference := range pod.OwnerReferences {
			if reference.Kind == "StatefulSet" && reference.Name == statefulSet.Name && reference.UID == statefulSet.UID && reference.Controller != nil && *reference.Controller {
				return pod
			}
		}
	}
	return nil
}
func gatewayAddress(prefix netip.Prefix) netip.Addr { return prefix.Masked().Addr().Next() }
func runtimeName(name string) string {
	value := "waycloak-gateway-" + name
	if len(value) <= 63 {
		return value
	}
	return value[:50] + "-" + shortHash(name)[:12]
}
func owner(gateway *wayv1.VPNGateway) metav1.OwnerReference {
	controller, block := true, true
	return metav1.OwnerReference{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNGateway", Name: gateway.Name, UID: gateway.UID, Controller: &controller, BlockOwnerDeletion: &block}
}

func ownedByGateway(references []metav1.OwnerReference, gateway *wayv1.VPNGateway) bool {
	for _, reference := range references {
		if reference.APIVersion == wayv1.GroupVersion.String() && reference.Kind == "VPNGateway" && reference.Name == gateway.Name && reference.UID == gateway.UID && reference.Controller != nil && *reference.Controller {
			return true
		}
	}
	return false
}

func requestsFeature(gateway *wayv1.VPNGateway, feature wayv1.FeatureName) bool {
	if gateway == nil {
		return false
	}
	for _, requested := range gateway.Spec.RequestedFeatures {
		if requested == feature {
			return true
		}
	}
	return false
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
func ValidExactImage(value string) bool {
	parts := strings.Split(value, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		return false
	}
	_, err := hex.DecodeString(parts[1])
	return err == nil
}
func (provisioner *Provisioner) reader() client.Reader {
	if provisioner.Reader != nil {
		return provisioner.Reader
	}
	return provisioner.Client
}
func (provisioner *Provisioner) httpClient() *http.Client {
	if provisioner.HTTPClient != nil {
		return provisioner.HTTPClient
	}
	return defaultHealthHTTPClient
}

func (provisioner *Provisioner) observeHealth(ctx context.Context, address string) (gatewaydataplane.HealthStatus, healthObservationResult, error) {
	started := time.Now()
	result := healthObservationResult{}
	for attempt := 1; attempt <= healthObservationAttempts; attempt++ {
		result.attempts = attempt
		attemptCtx, cancel := context.WithTimeout(ctx, healthObservationAttemptTimeout)
		request, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, address, nil)
		if err != nil {
			cancel()
			result.lastFailurePhase, result.lastFailureClass = "request_build", healthObservationErrorClass(err)
		} else {
			response, requestErr := provisioner.httpClient().Do(request)
			if requestErr != nil {
				result.lastFailurePhase, result.lastFailureClass = "exchange", healthObservationErrorClass(requestErr)
				cancel()
			} else {
				health, phase, class := decodeHealthResponse(response)
				cancel()
				if phase == "" {
					result.latency = time.Since(started)
					return health, result, nil
				}
				result.lastFailurePhase, result.lastFailureClass = phase, class
			}
		}
		if attempt < healthObservationAttempts && retryableHealthObservationFailure(result.lastFailurePhase, result.lastFailureClass) && waitForHealthRetry(ctx) {
			continue
		}
		break
	}
	result.latency = time.Since(started)
	failure := healthObservationError{phase: result.lastFailurePhase, class: result.lastFailureClass, attempts: result.attempts, latency: result.latency}
	return gatewaydataplane.HealthStatus{}, result, failure
}

func decodeHealthResponse(response *http.Response) (gatewaydataplane.HealthStatus, string, string) {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return gatewaydataplane.HealthStatus{}, "status", "http_" + strconv.Itoa(response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1025))
	decoder.DisallowUnknownFields()
	var health gatewaydataplane.HealthStatus
	if err := decoder.Decode(&health); err != nil {
		return gatewaydataplane.HealthStatus{}, "response_decode", healthObservationErrorClass(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return gatewaydataplane.HealthStatus{}, "response_validation", healthObservationErrorClass(err)
	}
	return health, "", ""
}

func waitForHealthRetry(ctx context.Context) bool {
	timer := time.NewTimer(healthObservationRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func retryableHealthObservationFailure(phase, class string) bool {
	return phase == "exchange" && (class == "eof" || class == "unexpected_eof" || class == "connection_reset" || class == "closed_idle_connection")
}

func healthObservationErrorClass(err error) string {
	if err == nil {
		return "invalid_response"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected_eof"
	}
	if errors.Is(err, io.EOF) {
		return "eof"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "connection reset") {
		return "connection_reset"
	}
	if strings.Contains(message, "closed idle connection") {
		return "closed_idle_connection"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "timeout"
		}
		return "network"
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return "invalid_json"
	}
	return "invalid_response"
}

func (provisioner *Provisioner) reportHealthObservationFailure(ctx context.Context, gateway *wayv1.VPNGateway, err error) {
	failure, ok := err.(healthObservationError)
	if !ok {
		failure = healthObservationError{phase: "unknown", class: "unknown", attempts: 1}
	}
	now := time.Now()
	provisioner.healthObservationMu.Lock()
	if provisioner.healthFailures == nil {
		provisioner.healthFailures = map[string]healthObservationFailure{}
	}
	previous, exists := provisioner.healthFailures[string(gateway.UID)]
	changed := !exists || previous.phase != failure.phase || previous.class != failure.class
	if !exists {
		previous.started = now
	}
	previous.phase, previous.class = failure.phase, failure.class
	provisioner.healthFailures[string(gateway.UID)] = previous
	provisioner.healthObservationMu.Unlock()
	if !changed {
		return
	}
	event := HealthObservationEvent{State: "not_ready", Phase: failure.phase, Class: failure.class, Attempts: failure.attempts, Latency: failure.latency}
	ctrllog.FromContext(ctx).Info("gateway_health_observation_transition", "gateway_namespace", gateway.Namespace, "gateway_name", gateway.Name, "state", event.State, "phase", event.Phase, "class", event.Class, "attempts", event.Attempts, "latency", event.Latency.Round(time.Millisecond))
	provisioner.emitHealthObservation(event)
}

func (provisioner *Provisioner) reportHealthObservationRecovery(ctx context.Context, gateway *wayv1.VPNGateway, result healthObservationResult) {
	provisioner.healthObservationMu.Lock()
	previous, existed := provisioner.healthFailures[string(gateway.UID)]
	if existed {
		delete(provisioner.healthFailures, string(gateway.UID))
	}
	provisioner.healthObservationMu.Unlock()
	if existed {
		event := HealthObservationEvent{State: "ready", Phase: previous.phase, Class: previous.class, Attempts: result.attempts, Latency: result.latency, UnavailableFor: time.Since(previous.started)}
		ctrllog.FromContext(ctx).Info("gateway_health_observation_transition", "gateway_namespace", gateway.Namespace, "gateway_name", gateway.Name, "state", event.State, "recovered", true, "previous_phase", event.Phase, "previous_class", event.Class, "attempts", event.Attempts, "latency", event.Latency.Round(time.Millisecond), "unavailable_for", event.UnavailableFor.Round(time.Millisecond))
		provisioner.emitHealthObservation(event)
		return
	}
	if result.attempts > 1 {
		event := HealthObservationEvent{State: "ready", Phase: result.lastFailurePhase, Class: result.lastFailureClass, Attempts: result.attempts, Latency: result.latency}
		ctrllog.FromContext(ctx).Info("gateway_health_observation_retry", "gateway_namespace", gateway.Namespace, "gateway_name", gateway.Name, "state", event.State, "recovered", true, "previous_phase", event.Phase, "previous_class", event.Class, "attempts", event.Attempts, "latency", event.Latency.Round(time.Millisecond))
		provisioner.emitHealthObservation(event)
	}
}

func (provisioner *Provisioner) emitHealthObservation(event HealthObservationEvent) {
	if provisioner.HealthObservationHook != nil {
		provisioner.HealthObservationHook(event)
	}
}

func pointer[T any](value T) *T                  { return &value }
func intstrFromInt(value int) intstr.IntOrString { return intstr.FromInt(value) }

var _ waycontroller.GatewayRuntimeProvisioner = (*Provisioner)(nil)
