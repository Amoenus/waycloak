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
	"os"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"github.com/Amoenus/waycloak/internal/gatewaydataplane"
	"github.com/Amoenus/waycloak/internal/portforward"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	dnsConfig := &corev1.ConfigMap{}
	must(t, kube.Get(context.Background(), client.ObjectKey{Namespace: "media", Name: "waycloak-gateway-private-dns"}, dnsConfig))
	corefile := dnsConfig.Data["Corefile"]
	for _, required := range []string{"cluster.local:1053", "bind 100.96.0.1", "forward . 10.43.0.10:53", ".:1053", "forward . 127.0.0.1:53", "max_concurrent 128", "failfast_all_unhealthy_upstreams"} {
		if !strings.Contains(corefile, required) {
			t.Fatalf("cluster-agnostic Corefile lacks %q:\n%s", required, corefile)
		}
	}
	if strings.Contains(corefile, "kubernetes") || strings.Contains(corefile, "kubeconfig") {
		t.Fatalf("gateway sidecar depends on Kubernetes DNS implementation details:\n%s", corefile)
	}
	if statefulSet.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
		t.Fatalf("gateway update strategy = %q, want explicit operator activation", statefulSet.Spec.UpdateStrategy.Type)
	}
	if statefulSet.Spec.Template.Annotations[releaseVersionAnnotation] != provisioner.ReleaseIdentity.Version || statefulSet.Spec.Template.Annotations[releaseManifestDigestAnnotation] != provisioner.ReleaseIdentity.ManifestDigest {
		t.Fatalf("gateway template is not bound to the exact release: %#v", statefulSet.Spec.Template.Annotations)
	}
	if len(statefulSet.Spec.VolumeClaimTemplates) != 0 || statefulSet.Spec.Template.Spec.Volumes[1].EmptyDir == nil {
		t.Fatalf("gateway StatefulSet unexpectedly carries durable application state: %#v", statefulSet.Spec)
	}
	statefulSet.UID = "statefulset-uid"
	must(t, kube.Update(context.Background(), statefulSet))
	if statefulSet.Spec.Template.Spec.AutomountServiceAccountToken == nil || *statefulSet.Spec.Template.Spec.AutomountServiceAccountToken || len(statefulSet.Spec.Template.Spec.Containers) != 3 {
		t.Fatalf("unsafe gateway Pod identity: %#v", statefulSet.Spec.Template.Spec)
	}
	engine, coreDNS, agent := statefulSet.Spec.Template.Spec.Containers[0], statefulSet.Spec.Template.Spec.Containers[1], statefulSet.Spec.Template.Spec.Containers[2]
	if engine.Name != "vpn-engine" || len(engine.Env) != 7 || engine.Env[2].Name != "VPN_PORT_FORWARDING" || engine.Env[2].Value != "off" || engine.Env[3].Name != "DNS_UPSTREAM_RESOLVER_TYPE" || engine.Env[3].Value != "doh" || engine.Env[4].Name != "DNS_UPSTREAM_RESOLVERS" || engine.Env[4].Value != "cloudflare,google,quad9" || engine.Env[5].Name != "DNS_CACHING" || engine.Env[5].Value != "on" || engine.Env[6].Name != "HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH" || engine.Env[6].Value != staticControlAuthConfigPath || agent.Name != "gateway-agent" || len(agent.Env) != 0 || len(agent.VolumeMounts) != 0 || len(statefulSet.Spec.Template.Spec.InitContainers) != 1 || statefulSet.Spec.Template.Spec.InitContainers[0].Name != "gateway-dataplane-init" {
		t.Fatalf("credential boundary is unsafe: engine=%#v agent=%#v", engine, agent)
	}
	if coreDNS.Name != "coredns" || coreDNS.Image != provisioner.CoreDNSImage || len(coreDNS.Ports) != 2 || coreDNS.Ports[0].Protocol != corev1.ProtocolUDP || coreDNS.Ports[1].Protocol != corev1.ProtocolTCP || len(coreDNS.VolumeMounts) != 1 || coreDNS.VolumeMounts[0].MountPath != coreDNSConfigPath || coreDNS.VolumeMounts[0].SubPath != "Corefile" || coreDNS.SecurityContext == nil || coreDNS.SecurityContext.RunAsNonRoot == nil || !*coreDNS.SecurityContext.RunAsNonRoot || coreDNS.SecurityContext.Capabilities == nil || len(coreDNS.SecurityContext.Capabilities.Add) != 1 || coreDNS.SecurityContext.Capabilities.Add[0] != "NET_BIND_SERVICE" || len(coreDNS.SecurityContext.Capabilities.Drop) != 1 || coreDNS.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("CoreDNS sidecar boundary is unsafe: %#v", coreDNS)
	}
	if engine.SecurityContext == nil || engine.SecurityContext.Capabilities == nil || len(engine.SecurityContext.Capabilities.Add) != 5 || engine.SecurityContext.Capabilities.Add[0] != "NET_ADMIN" || engine.SecurityContext.Capabilities.Add[1] != "CHOWN" || engine.SecurityContext.Capabilities.Add[2] != "DAC_OVERRIDE" || engine.SecurityContext.Capabilities.Add[3] != "SETUID" || engine.SecurityContext.Capabilities.Add[4] != "KILL" {
		t.Fatalf("VPN engine lacks its exact runtime capabilities: %#v", engine.SecurityContext)
	}
	if agent.SecurityContext == nil || agent.SecurityContext.Capabilities == nil || len(agent.SecurityContext.Capabilities.Add) != 1 || agent.SecurityContext.Capabilities.Add[0] != "NET_ADMIN" {
		t.Fatalf("gateway agent capabilities were broadened: %#v", agent.SecurityContext)
	}
	if len(coreDNS.Ports) != 2 || coreDNS.Ports[0].ContainerPort != int32(gatewaydataplane.DNSListenPort) || coreDNS.Ports[1].ContainerPort != int32(gatewaydataplane.DNSListenPort) {
		t.Fatalf("CoreDNS listener does not match the workload redirect: %#v", coreDNS.Ports)
	}
	if agent.ReadinessProbe == nil || agent.ReadinessProbe.HTTPGet == nil || agent.ReadinessProbe.HTTPGet.Path != "/readyz" {
		t.Fatalf("gateway readiness probe does not use the workload-observed contract: %#v", agent.ReadinessProbe)
	}
	if statefulSet.Spec.Template.Spec.DNSConfig == nil || len(statefulSet.Spec.Template.Spec.DNSConfig.Options) != 1 || statefulSet.Spec.Template.Spec.DNSConfig.Options[0].Name != "ndots" || statefulSet.Spec.Template.Spec.DNSConfig.Options[0].Value == nil || *statefulSet.Spec.Template.Spec.DNSConfig.Options[0].Value != "1" {
		t.Fatalf("gateway Pod does not bound Kubernetes DNS search expansion: %#v", statefulSet.Spec.Template.Spec.DNSConfig)
	}
	if statefulSet.Spec.Template.Spec.DNSPolicy == corev1.DNSNone || len(statefulSet.Spec.Template.Spec.DNSConfig.Nameservers) != 0 {
		t.Fatalf("gateway Pod replaced the initial cluster nameserver: policy=%q config=%#v", statefulSet.Spec.Template.Spec.DNSPolicy, statefulSet.Spec.Template.Spec.DNSConfig)
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
	provisioner.HTTPClient = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ready":false,"tunnelReady":true,"dnsReady":false}`)), Header: http.Header{}}, nil
	})}
	observation, err = provisioner.Reconcile(context.Background(), gateway)
	if err != nil || observation.Ready || !observation.TunnelReady || observation.DNSReady || observation.MembershipApplied {
		t.Fatalf("DNS-specific failure was not preserved in gateway observation: %#v %v", observation, err)
	}
}

func TestProvisionerCorrectsRollingUpdateWithoutReplacingGatewayPod(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "media", UID: "gateway-uid", Generation: 1}, Spec: wayv1.VPNGatewaySpec{GatewayClassName: "gluetun.waycloak.io", NativeConfigRefs: []wayv1.RoleObjectReference{{Role: waycontroller.GluetunEnvironmentRole, Name: "engine"}}, CredentialRefs: []wayv1.RoleObjectReference{{Role: waycontroller.OpenVPNCredentialsRole, Name: "credentials"}}, ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll}}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "engine", Namespace: "media", ResourceVersion: "1"}, Data: map[string]string{"VPN_SERVICE_PROVIDER": "protonvpn", "VPN_TYPE": "openvpn"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "media", ResourceVersion: "1"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway, config, secret).Build()
	provisioner := fixture(kube)
	if _, err := provisioner.Reconcile(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	statefulSet := &appsv1.StatefulSet{}
	must(t, kube.Get(context.Background(), client.ObjectKey{Namespace: "media", Name: "waycloak-gateway-private"}, statefulSet))
	statefulSet.UID = "statefulset-uid"
	statefulSet.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}
	must(t, kube.Update(context.Background(), statefulSet))
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "waycloak-gateway-private-0", Namespace: "media", UID: "gateway-pod-uid", Annotations: map[string]string{releaseVersionAnnotation: statefulSet.Spec.Template.Annotations[releaseVersionAnnotation], releaseManifestDigestAnnotation: statefulSet.Spec.Template.Annotations[releaseManifestDigestAnnotation]}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: statefulSet.Name, UID: statefulSet.UID, Controller: &controller}}}, Spec: *statefulSet.Spec.Template.Spec.DeepCopy()}
	must(t, kube.Create(context.Background(), pod))
	oldEngineImage := pod.Spec.Containers[0].Image
	oldReleaseVersion := pod.Annotations[releaseVersionAnnotation]
	oldReleaseDigest := pod.Annotations[releaseManifestDigestAnnotation]
	provisioner.EngineImage = "docker.io/qmcgaw/gluetun@sha256:" + strings.Repeat("c", 64)
	provisioner.AgentImage = "ghcr.io/amoenus/waycloak-gateway-agent@sha256:" + strings.Repeat("d", 64)
	provisioner.ReleaseIdentity = wayv1.ReleaseIdentity{Version: "v0.0.0-core.next", ManifestDigest: "sha256:" + strings.Repeat("e", 64)}
	if _, err := provisioner.Reconcile(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	must(t, kube.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), statefulSet))
	if statefulSet.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
		t.Fatalf("existing gateway update strategy = %q after correction", statefulSet.Spec.UpdateStrategy.Type)
	}
	if got := statefulSet.Spec.Template.Spec.Containers[0].Image; got != provisioner.EngineImage {
		t.Fatalf("desired gateway template image = %q, want %q", got, provisioner.EngineImage)
	}
	if statefulSet.Spec.Template.Annotations[releaseVersionAnnotation] != provisioner.ReleaseIdentity.Version || statefulSet.Spec.Template.Annotations[releaseManifestDigestAnnotation] != provisioner.ReleaseIdentity.ManifestDigest {
		t.Fatalf("desired gateway template release = %#v, want %#v", statefulSet.Spec.Template.Annotations, provisioner.ReleaseIdentity)
	}
	currentPod := &corev1.Pod{}
	must(t, kube.Get(context.Background(), client.ObjectKeyFromObject(pod), currentPod))
	if currentPod.UID != pod.UID || currentPod.Spec.Containers[0].Image != oldEngineImage || currentPod.Annotations[releaseVersionAnnotation] != oldReleaseVersion || currentPod.Annotations[releaseManifestDigestAnnotation] != oldReleaseDigest {
		t.Fatalf("existing gateway Pod changed during OnDelete correction: uid=%q image=%q release=%q digest=%q", currentPod.UID, currentPod.Spec.Containers[0].Image, currentPod.Annotations[releaseVersionAnnotation], currentPod.Annotations[releaseManifestDigestAnnotation])
	}
}

func TestProvisionerAddsOnlyExplicitTokenlessPortForwardRuntime(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "media", UID: "gateway-uid", Generation: 1}, Spec: wayv1.VPNGatewaySpec{
		GatewayClassName: "gluetun.waycloak.io", RequestedFeatures: []wayv1.FeatureName{wayv1.FeaturePortForwardSingleActive, wayv1.FeatureWorkloadAdapter},
		NativeConfigRefs: []wayv1.RoleObjectReference{{Role: waycontroller.GluetunEnvironmentRole, Name: "engine"}},
		CredentialRefs:   []wayv1.RoleObjectReference{{Role: waycontroller.OpenVPNCredentialsRole, Name: "credentials"}, {Role: waycontroller.GatewayRuntimeTLSRole, Name: "runtime-tls"}},
		ClusterTraffic:   wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
	}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "engine", Namespace: "media", ResourceVersion: "1"}, Data: map[string]string{"VPN_SERVICE_PROVIDER": "protonvpn", "VPN_TYPE": "openvpn"}}
	credentials := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "media", ResourceVersion: "1"}}
	runtimeTLS := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "runtime-tls", Namespace: "media", ResourceVersion: "2"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway, config, credentials, runtimeTLS).Build()
	provisioner := fixture(kube)
	provisioner.PortForwardRuntimeImage = "ghcr.io/amoenus/waycloak-gateway-runtime@sha256:" + strings.Repeat("d", 64)
	provisioner.PortForwardRuntimePort = 9443
	provisioner.AdapterPort = 9444
	provisioner.AdapterEnabled = true
	provisioner.OTLPEndpoint = "http://otel-collector.monitoring.svc:4318"
	provisioner.OTelQueueSize = 128
	provisioner.OTelExportInterval = 30 * time.Second
	provisioner.OTelExportTimeout = 500 * time.Millisecond
	if _, err := provisioner.Reconcile(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	statefulSet := &appsv1.StatefulSet{}
	must(t, kube.Get(context.Background(), client.ObjectKey{Namespace: "media", Name: "waycloak-gateway-private"}, statefulSet))
	if len(statefulSet.Spec.Template.Spec.Containers) != 4 || len(statefulSet.Spec.Template.Spec.InitContainers) != 2 || len(statefulSet.Spec.Template.Spec.Volumes) != 5 {
		t.Fatalf("port-forward gateway Pod shape = %d containers, %d init containers, %d volumes", len(statefulSet.Spec.Template.Spec.Containers), len(statefulSet.Spec.Template.Spec.InitContainers), len(statefulSet.Spec.Template.Spec.Volumes))
	}
	initContainer := statefulSet.Spec.Template.Spec.InitContainers[1]
	if initContainer.Name != "gluetun-control-auth" || initContainer.Image != provisioner.PortForwardRuntimeImage || len(initContainer.VolumeMounts) != 2 || !strings.Contains(strings.Join(initContainer.Args, " "), "--write-gluetun-control-auth="+generatedControlAuthConfigPath) || !strings.Contains(strings.Join(initContainer.Args, " "), "--gluetun-control-api-key-output="+generatedControlAPIKeyPath) {
		t.Fatalf("authenticated control policy init container is not exact: %#v", initContainer)
	}
	engine, agent := statefulSet.Spec.Template.Spec.Containers[0], statefulSet.Spec.Template.Spec.Containers[2]
	if len(engine.Env) != 11 || engine.Env[2].Name != "VPN_PORT_FORWARDING" || engine.Env[2].Value != "on" || engine.Env[3].Name != "DNS_UPSTREAM_RESOLVER_TYPE" || engine.Env[3].Value != "doh" || engine.Env[4].Name != "DNS_UPSTREAM_RESOLVERS" || engine.Env[4].Value != "cloudflare,google,quad9" || engine.Env[5].Name != "DNS_CACHING" || engine.Env[5].Value != "on" || engine.Env[6].Name != "VPN_PORT_FORWARDING_STATUS_FILE" || engine.Env[6].Value != "/gluetun/forwarded_port" || engine.Env[10].Name != "HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH" || engine.Env[10].Value != generatedControlAuthConfigPath || len(engine.VolumeMounts) != 3 {
		t.Fatalf("Gluetun native port forwarding is not release-owned: %#v", engine)
	}
	agentArgs := strings.Join(agent.Args, " ")
	if len(agent.VolumeMounts) != 1 || !strings.Contains(agentArgs, "--gluetun-control-api-key-file="+generatedControlAPIKeyPath) || !strings.Contains(agentArgs, "--otel-otlp-endpoint=http://otel-collector.monitoring.svc:4318") || !strings.Contains(agentArgs, "--otel-queue-size=128") {
		t.Fatalf("gateway observer lacks authenticated control access: %#v", agent)
	}
	runtimeContainer := statefulSet.Spec.Template.Spec.Containers[3]
	if runtimeContainer.Name != "port-forward-runtime" || runtimeContainer.Image != provisioner.PortForwardRuntimeImage || len(runtimeContainer.Env) != 0 || len(runtimeContainer.EnvFrom) != 0 || len(runtimeContainer.VolumeMounts) != 2 || runtimeContainer.VolumeMounts[0].Name != "port-forward-runtime-tls" || runtimeContainer.VolumeMounts[1].Name != "gluetun-control-auth" {
		t.Fatalf("unsafe tokenless runtime container: %#v", runtimeContainer)
	}
	joinedArgs := strings.Join(runtimeContainer.Args, " ")
	for _, required := range []string{"--gateway-uid=gateway-uid", "--engine-port-forward-capability=gluetun.waycloak.io/native-port-forward", "--gluetun-control-api-key-file=" + generatedControlAPIKeyPath, "--tls-cert=" + portForwardTLSMountPath + "/tls.crt", "--adapter-client-cert=" + portForwardTLSMountPath + "/adapter-client.crt", "--cluster-domain=cluster.local", "--otel-otlp-endpoint=http://otel-collector.monitoring.svc:4318", "--otel-queue-size=128", "--otel-export-interval=30s", "--otel-export-timeout=500ms"} {
		if !strings.Contains(joinedArgs, required) {
			t.Fatalf("runtime args %q lack %q", joinedArgs, required)
		}
	}
	if strings.Contains(joinedArgs, "OPENVPN") || strings.Contains(joinedArgs, "credentials") {
		t.Fatalf("runtime args expose VPN credential identity: %q", joinedArgs)
	}
	service := &corev1.Service{}
	must(t, kube.Get(context.Background(), client.ObjectKey{Namespace: "media", Name: portforward.GatewayRuntimeServiceName("media", "private")}, service))
	if len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 9443 || !ownedByGateway(service.OwnerReferences, gateway) {
		t.Fatalf("runtime Service is not exact gateway-owned identity: %#v", service)
	}

	gateway.Spec.RequestedFeatures = nil
	gateway.Spec.CredentialRefs = []wayv1.RoleObjectReference{{Role: waycontroller.OpenVPNCredentialsRole, Name: "credentials"}}
	if _, err := provisioner.Reconcile(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(service), service); !apierrors.IsNotFound(err) {
		t.Fatalf("unrequested runtime Service was retained: %v", err)
	}
	must(t, kube.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), statefulSet))
	if len(statefulSet.Spec.Template.Spec.Containers) != 3 || len(statefulSet.Spec.Template.Spec.InitContainers) != 1 || len(statefulSet.Spec.Template.Spec.Volumes) != 3 {
		t.Fatalf("baseline gateway retained port-forward runtime: %#v", statefulSet.Spec.Template.Spec)
	}
}

func TestProvisionerDoesNotDeleteForeignRuntimeService(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "media", UID: "gateway-uid", Generation: 1}, Spec: wayv1.VPNGatewaySpec{
		GatewayClassName: "gluetun.waycloak.io",
		NativeConfigRefs: []wayv1.RoleObjectReference{{Role: waycontroller.GluetunEnvironmentRole, Name: "engine"}},
		CredentialRefs:   []wayv1.RoleObjectReference{{Role: waycontroller.OpenVPNCredentialsRole, Name: "credentials"}},
		ClusterTraffic:   wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
	}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "engine", Namespace: "media", ResourceVersion: "1"}, Data: map[string]string{"VPN_SERVICE_PROVIDER": "protonvpn", "VPN_TYPE": "openvpn"}}
	credentials := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "media", ResourceVersion: "1"}}
	foreign := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: portforward.GatewayRuntimeServiceName("media", "private"), Namespace: "media", UID: "foreign-service"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway, config, credentials, foreign).Build()
	provisioner := fixture(kube)
	if _, err := provisioner.Reconcile(context.Background(), gateway); err == nil || !strings.Contains(err.Error(), "owned by another object") {
		t.Fatalf("foreign runtime Service collision error = %v", err)
	}
	current := &corev1.Service{}
	must(t, kube.Get(context.Background(), client.ObjectKeyFromObject(foreign), current))
	if current.UID != foreign.UID {
		t.Fatalf("foreign runtime Service was replaced: %#v", current)
	}
}

func TestProvisionerRejectsUnsupportedEnginePortForwardCapability(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "media", UID: "gateway-uid", Generation: 1}, Spec: wayv1.VPNGatewaySpec{
		GatewayClassName: "gluetun.waycloak.io", RequestedFeatures: []wayv1.FeatureName{wayv1.FeaturePortForwardSingleActive},
		NativeConfigRefs: []wayv1.RoleObjectReference{{Role: waycontroller.GluetunEnvironmentRole, Name: "engine"}},
		CredentialRefs:   []wayv1.RoleObjectReference{{Role: waycontroller.OpenVPNCredentialsRole, Name: "credentials"}, {Role: waycontroller.GatewayRuntimeTLSRole, Name: "runtime-tls"}},
		ClusterTraffic:   wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
	}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "engine", Namespace: "media", ResourceVersion: "1"}, Data: map[string]string{"VPN_SERVICE_PROVIDER": "mullvad", "VPN_TYPE": "wireguard"}}
	credentials := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "media", ResourceVersion: "1"}}
	runtimeTLS := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "runtime-tls", Namespace: "media", ResourceVersion: "2"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway, config, credentials, runtimeTLS).Build()
	provisioner := fixture(kube)
	provisioner.PortForwardRuntimeImage = "ghcr.io/amoenus/waycloak-gateway-runtime@sha256:" + strings.Repeat("d", 64)
	provisioner.PortForwardRuntimePort = 9443
	provisioner.AdapterPort = 9444
	if _, err := provisioner.Reconcile(context.Background(), gateway); err == nil || !strings.Contains(err.Error(), "qualified port-forward capability") {
		t.Fatalf("unsupported Gluetun port-forward configuration error = %v", err)
	}
	statefulSet := &appsv1.StatefulSet{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "media", Name: "waycloak-gateway-private"}, statefulSet); !apierrors.IsNotFound(err) {
		t.Fatalf("unsupported capability rendered gateway state: %v", err)
	}
}

func TestProvisionerRejectsPartialPortForwardConfiguration(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	provisioner := fixture(fake.NewClientBuilder().WithScheme(scheme).Build())
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{UID: "gateway-uid"}}
	provisioner.PortForwardRuntimeImage = "ghcr.io/amoenus/waycloak-gateway-runtime@sha256:" + strings.Repeat("d", 64)
	if err := provisioner.validate(gateway); err == nil || !strings.Contains(err.Error(), "complete exact port-forward") {
		t.Fatalf("partial port-forward configuration error = %v", err)
	}
}

func TestValidateEngineConfigRejectsControlAuthenticationOverrides(t *testing.T) {
	for _, key := range []string{"HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", "HTTP_CONTROL_SERVER_AUTH_DEFAULT_ROLE", "VPN_PORT_FORWARDING", "VPN_PORT_FORWARDING_PROVIDER", "VPN_PORT_FORWARDING_STATUS_FILE", "VPN_PORT_FORWARDING_UP_COMMAND", "VPN_PORT_FORWARDING_DOWN_COMMAND", "VPN_PORT_FORWARDING_LISTENING_PORT"} {
		if err := validateEngineConfig(map[string]string{key: "unsafe"}); err == nil {
			t.Fatalf("accepted operator override of %s", key)
		}
	}
	if err := validateEngineConfig(map[string]string{"VPN_SERVICE_PROVIDER": "protonvpn"}); err != nil {
		t.Fatalf("rejected ordinary native engine configuration: %v", err)
	}
}

func TestGluetunDNSEnvironmentUsesQualifiedDefaultsAndPreservesOverrides(t *testing.T) {
	defaults := gluetunDNSEnvironment(nil)
	if len(defaults) != 3 || defaults[0].Value != "doh" || defaults[1].Value != "cloudflare,google,quad9" || defaults[2].Value != "on" {
		t.Fatalf("qualified DNS defaults = %#v", defaults)
	}

	overrides := gluetunDNSEnvironment(map[string]string{
		"DNS_UPSTREAM_RESOLVER_TYPE": "dot",
		"DNS_UPSTREAM_RESOLVERS":     "quad9",
		"DNS_CACHING":                "off",
	})
	if len(overrides) != 3 || overrides[0].Value != "dot" || overrides[1].Value != "quad9" || overrides[2].Value != "off" {
		t.Fatalf("configured DNS policy = %#v", overrides)
	}
}

func TestSignedEngineControlPolicyExposesOnlyReadObservations(t *testing.T) {
	policy, err := os.ReadFile("../../build/gluetun-candidate/control-auth.toml")
	if err != nil {
		t.Fatal(err)
	}
	wanted := "# Copyright 2026 The Waycloak Authors.\n# SPDX-License-Identifier: MIT\n\n[[roles]]\nname = \"waycloak-observer\"\nroutes = [\"GET /v1/dns/status\", \"GET /v1/publicip/ip\"]\nauth = \"none\"\n"
	if string(policy) != wanted {
		t.Fatalf("Gluetun control policy broadened beyond exact read observations:\n%s", policy)
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
	provisioner = fixture(kube)
	provisioner.ReleaseIdentity.ManifestDigest = "sha256:not-exact"
	if _, err := provisioner.Reconcile(context.Background(), &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "default", UID: "uid"}}); err == nil {
		t.Fatal("invalid gateway release identity accepted")
	}
}

func TestHealthObservationRetriesTransportFailureButNotObservedUnready(t *testing.T) {
	calls := 0
	provisioner := &Provisioner{HTTPClient: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ready":false,"tunnelReady":true,"dnsReady":false}`)), Header: http.Header{}}, nil
	})}}
	health, result, err := provisioner.observeHealth(context.Background(), "http://127.0.0.1/status")
	if err != nil || calls != 2 || result.attempts != 2 || result.lastFailurePhase != "exchange" || result.lastFailureClass != "unexpected_eof" {
		t.Fatalf("bounded retry result = health=%#v result=%#v calls=%d err=%v", health, result, calls, err)
	}
	if health.Ready || !health.TunnelReady || health.DNSReady {
		t.Fatalf("observed unhealthy response was altered: %#v", health)
	}

	calls = 0
	provisioner.HTTPClient = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ready":false,"tunnelReady":true,"dnsReady":false}`)), Header: http.Header{}}, nil
	})}
	health, result, err = provisioner.observeHealth(context.Background(), "http://127.0.0.1/status")
	if err != nil || calls != 1 || result.attempts != 1 || health.Ready || !health.TunnelReady || health.DNSReady {
		t.Fatalf("observed not-ready response was retried or altered: health=%#v result=%#v calls=%d err=%v", health, result, calls, err)
	}

	calls = 0
	provisioner.HTTPClient = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, context.DeadlineExceeded
	})}
	_, result, err = provisioner.observeHealth(context.Background(), "http://127.0.0.1/status")
	failure, ok := err.(healthObservationError)
	if !ok || calls != 1 || result.attempts != 1 || failure.phase != "exchange" || failure.class != "timeout" {
		t.Fatalf("non-retryable timeout = failure=%#v result=%#v calls=%d err=%v", failure, result, calls, err)
	}
}

func TestHealthObservationReportsSanitizedFailureAndRecoveryTransitions(t *testing.T) {
	events := []HealthObservationEvent{}
	provisioner := &Provisioner{HealthObservationHook: func(event HealthObservationEvent) { events = append(events, event) }}
	provisioner.HTTPClient = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "media", UID: "gateway-uid"}}
	_, result, err := provisioner.observeHealth(context.Background(), "http://127.0.0.1/status")
	if err == nil || result.attempts != healthObservationAttempts || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("terminal health observation error = %v, result=%#v", err, result)
	}
	provisioner.reportHealthObservationFailure(context.Background(), gateway, err)
	provisioner.reportHealthObservationFailure(context.Background(), gateway, err)
	if len(events) != 1 || events[0].State != "not_ready" || events[0].Phase != "exchange" || events[0].Class != "unexpected_eof" || events[0].Attempts != healthObservationAttempts {
		t.Fatalf("failure transitions = %#v", events)
	}

	provisioner.HTTPClient = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ready":true,"tunnelReady":true,"dnsReady":true}`)), Header: http.Header{}}, nil
	})}
	health, result, err := provisioner.observeHealth(context.Background(), "http://127.0.0.1/status")
	if err != nil || !health.Ready {
		t.Fatalf("recovery observation = %#v, result=%#v, err=%v", health, result, err)
	}
	provisioner.reportHealthObservationRecovery(context.Background(), gateway, result)
	if len(events) != 2 || events[1].State != "ready" || events[1].Phase != "exchange" || events[1].Class != "unexpected_eof" {
		t.Fatalf("recovery transitions = %#v", events)
	}
}

func TestHealthObservationClassifiesStatusAndResponseFailures(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		code  int
		phase string
		class string
	}{
		{name: "status", code: http.StatusServiceUnavailable, phase: "status", class: "http_503"},
		{name: "decode", code: http.StatusOK, body: `{`, phase: "response_decode", class: "unexpected_eof"},
		{name: "trailing", code: http.StatusOK, body: `{"ready":true,"tunnelReady":true,"dnsReady":true}{}`, phase: "response_validation", class: "invalid_response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provisioner := &Provisioner{HTTPClient: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.code, Body: io.NopCloser(strings.NewReader(test.body)), Header: http.Header{}}, nil
			})}}
			_, _, err := provisioner.observeHealth(context.Background(), "http://127.0.0.1/status")
			failure, ok := err.(healthObservationError)
			if !ok || failure.phase != test.phase || failure.class != test.class || failure.attempts != 1 {
				t.Fatalf("classified failure = %#v (%v)", failure, err)
			}
		})
	}
}

func fixture(kube client.Client) *Provisioner {
	return &Provisioner{Client: kube, Reader: kube, EngineImage: "docker.io/qmcgaw/gluetun@sha256:" + strings.Repeat("a", 64), AgentImage: "ghcr.io/amoenus/waycloak-gateway-agent@sha256:" + strings.Repeat("b", 64), CoreDNSImage: "docker.io/coredns/coredns@sha256:" + strings.Repeat("d", 64), ReleaseIdentity: wayv1.ReleaseIdentity{Version: "v0.0.0-core.test", ManifestDigest: "sha256:" + strings.Repeat("c", 64)}, OverlayCIDR: netip.MustParsePrefix("100.96.0.0/24"), ClusterDNSUpstream: netip.MustParseAddrPort("10.43.0.10:53"), ClusterDomain: "cluster.local", VNI: 7999, MTU: 1320, VXLANPort: 4789, HealthPort: 18080, HTTPClient: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ready":true,"tunnelReady":true,"dnsReady":true}`)), Header: http.Header{}}, nil
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
