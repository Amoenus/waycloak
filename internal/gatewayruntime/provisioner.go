// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewayruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"github.com/Amoenus/waycloak/internal/gatewaydataplane"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	managedLabel          = "runtime.networking.waycloak.io/gateway-uid"
	controlAuthConfigPath = "/etc/waycloak/gluetun-control-auth.toml"
)

type Provisioner struct {
	Client      client.Client
	Reader      client.Reader
	EngineImage string
	AgentImage  string
	OverlayCIDR netip.Prefix
	VNI         uint32
	MTU         int32
	VXLANPort   uint16
	HealthPort  uint16
	HTTPClient  *http.Client
}

func (provisioner *Provisioner) Reconcile(ctx context.Context, gateway *wayv1.VPNGateway) (waycontroller.GatewayRuntimeObservation, error) {
	if err := provisioner.validate(gateway); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	configName, secretName, err := inputNames(gateway)
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
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
	if err := provisioner.reader().Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: secretName}, secret); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	name := runtimeName(gateway.Name)
	labels := map[string]string{"app.kubernetes.io/name": "waycloak-gateway", "app.kubernetes.io/component": "gateway", "app.kubernetes.io/instance": name, managedLabel: shortHash(string(gateway.UID))}
	service := provisioner.desiredService(gateway, name, labels)
	if err := provisioner.reconcileObject(ctx, service); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	statefulSet := provisioner.desiredStatefulSet(gateway, name, labels, configName, secretName, configMap, secret)
	if err := provisioner.reconcileObject(ctx, statefulSet); err != nil {
		return waycontroller.GatewayRuntimeObservation{}, err
	}
	observation := waycontroller.GatewayRuntimeObservation{Programmed: true, Addresses: provisioner.addresses("")}
	pods := &corev1.PodList{}
	if err := provisioner.reader().List(ctx, pods, client.InNamespace(gateway.Namespace), client.MatchingLabels{managedLabel: shortHash(string(gateway.UID))}); err != nil {
		return observation, err
	}
	current := currentPod(pods.Items, statefulSet)
	if current == nil || current.Status.PodIP == "" || !podReady(current) {
		return observation, nil
	}
	endpoint, err := netip.ParseAddr(current.Status.PodIP)
	if err != nil || !endpoint.Is4() {
		return observation, nil
	}
	observation.Addresses = provisioner.addresses(netip.AddrPortFrom(endpoint, provisioner.VXLANPort).String())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+netip.AddrPortFrom(endpoint, provisioner.HealthPort).String()+"/", nil)
	if err != nil {
		return observation, err
	}
	response, err := provisioner.httpClient().Do(request)
	if err != nil {
		return observation, nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return observation, nil
	}
	observation.Ready = true
	observation.TunnelReady = true
	observation.DNSReady = true
	observation.MembershipApplied = true
	return observation, nil
}

func (provisioner *Provisioner) validate(gateway *wayv1.VPNGateway) error {
	if provisioner.Client == nil || gateway == nil || gateway.UID == "" || !exactImage(provisioner.EngineImage) || !exactImage(provisioner.AgentImage) || !provisioner.OverlayCIDR.IsValid() || !provisioner.OverlayCIDR.Addr().Is4() || provisioner.OverlayCIDR.Bits() < 16 || provisioner.OverlayCIDR.Bits() > 29 || provisioner.VNI == 0 || provisioner.VNI > 16777215 || provisioner.MTU < 576 || provisioner.MTU > 9000 || provisioner.VXLANPort == 0 || provisioner.HealthPort == 0 {
		return errors.New("exact gateway runtime images and reviewed network parameters are required")
	}
	return nil
}
func inputNames(gateway *wayv1.VPNGateway) (string, string, error) {
	var config, secret string
	for _, ref := range gateway.Spec.NativeConfigRefs {
		if ref.Role == waycontroller.GluetunEnvironmentRole {
			if config != "" {
				return "", "", errors.New("multiple Gluetun environment references are unsupported")
			}
			config = string(ref.Name)
		}
	}
	for _, ref := range gateway.Spec.CredentialRefs {
		if ref.Role == waycontroller.OpenVPNCredentialsRole {
			if secret != "" {
				return "", "", errors.New("multiple OpenVPN credential references are unsupported")
			}
			secret = string(ref.Name)
		}
	}
	if config == "" || secret == "" {
		return "", "", errors.New("one native configuration and credential reference are required")
	}
	return config, secret, nil
}

func validateEngineConfig(data map[string]string) error {
	for _, key := range []string{"HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", "HTTP_CONTROL_SERVER_AUTH_DEFAULT_ROLE"} {
		if _, exists := data[key]; exists {
			return errors.New("gluetun control authentication is release-owned")
		}
	}
	return nil
}

func (provisioner *Provisioner) desiredService(gateway *wayv1.VPNGateway, name string, labels map[string]string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateway.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner(gateway)}}, Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Selector: labels, Ports: []corev1.ServicePort{{Name: "health", Port: int32(provisioner.HealthPort), Protocol: corev1.ProtocolTCP}}}}
}
func (provisioner *Provisioner) desiredStatefulSet(gateway *wayv1.VPNGateway, name string, labels map[string]string, configName, secretName string, configMap *corev1.ConfigMap, secret *metav1.PartialObjectMetadata) *appsv1.StatefulSet {
	replicas := int32(1)
	no := false
	yes := true
	runAsRoot := int64(0)
	hash := sha256.Sum256([]byte(configMap.ResourceVersion + "\x00" + secret.GetResourceVersion()))
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateway.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner(gateway)}}, Spec: appsv1.StatefulSetSpec{ServiceName: name, Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{"runtime.networking.waycloak.io/input-revision": hex.EncodeToString(hash[:])}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, TerminationGracePeriodSeconds: pointer(int64(20)), NodeSelector: gateway.Spec.Placement.NodeSelector, Tolerations: gateway.Spec.Placement.Tolerations, DNSConfig: &corev1.PodDNSConfig{Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: pointer("1")}}}, Containers: []corev1.Container{
		{Name: "vpn-engine", Image: provisioner.EngineImage, ImagePullPolicy: corev1.PullIfNotPresent, EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}}, Env: []corev1.EnvVar{{Name: "OPENVPN_USER", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "username"}}}, {Name: "OPENVPN_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "password"}}}, {Name: "HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", Value: controlAuthConfigPath}}, SecurityContext: &corev1.SecurityContext{RunAsUser: &runAsRoot, RunAsGroup: &runAsRoot, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &no, Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "CHOWN", "DAC_OVERRIDE", "SETUID", "KILL"}, Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "tun", MountPath: "/dev/net/tun"}, {Name: "engine-state", MountPath: "/gluetun"}}},
		{Name: "gateway-agent", Image: provisioner.AgentImage, ImagePullPolicy: corev1.PullIfNotPresent, Args: []string{"--gateway-uid=" + string(gateway.UID), "--overlay-cidr=" + provisioner.OverlayCIDR.Masked().String(), "--gateway-address=" + gatewayAddress(provisioner.OverlayCIDR).String(), "--vxlan-port=" + strconv.Itoa(int(provisioner.VXLANPort)), "--health-port=" + strconv.Itoa(int(provisioner.HealthPort)), "--vni=" + strconv.FormatUint(uint64(provisioner.VNI), 10), "--mtu=" + strconv.Itoa(int(provisioner.MTU))}, Ports: []corev1.ContainerPort{{Name: "vxlan", ContainerPort: int32(provisioner.VXLANPort), Protocol: corev1.ProtocolUDP}, {Name: "health", ContainerPort: int32(provisioner.HealthPort), Protocol: corev1.ProtocolTCP}, {Name: "dns-udp", ContainerPort: int32(gatewaydataplane.DNSListenPort), Protocol: corev1.ProtocolUDP}, {Name: "dns-tcp", ContainerPort: int32(gatewaydataplane.DNSListenPort), Protocol: corev1.ProtocolTCP}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstrFromInt(int(provisioner.HealthPort))}}, PeriodSeconds: 2, FailureThreshold: 2}, SecurityContext: &corev1.SecurityContext{RunAsUser: &runAsRoot, RunAsGroup: &runAsRoot, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}, Drop: []corev1.Capability{"ALL"}}}},
	}, Volumes: []corev1.Volume{{Name: "tun", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev/net/tun", Type: pointer(corev1.HostPathCharDev)}}}, {Name: "engine-state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}}}}
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
	case *appsv1.StatefulSet:
		existing := current.(*appsv1.StatefulSet)
		updated := existing.DeepCopy()
		updated.Labels, updated.OwnerReferences = desired.Labels, desired.OwnerReferences
		updated.Spec.ServiceName, updated.Spec.Replicas, updated.Spec.Selector, updated.Spec.Template = desired.Spec.ServiceName, desired.Spec.Replicas, desired.Spec.Selector, desired.Spec.Template
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
func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
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
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
func exactImage(value string) bool {
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
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
}
func pointer[T any](value T) *T                  { return &value }
func intstrFromInt(value int) intstr.IntOrString { return intstr.FromInt(value) }

var _ waycontroller.GatewayRuntimeProvisioner = (*Provisioner)(nil)
