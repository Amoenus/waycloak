// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/Amoenus/waycloak/internal/delivery"
	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	realPortForwardEngineImage = "docker.io/qmcgaw/gluetun:v3.41.0@sha256:6b54856716d0de56e5bb00a77029b0adea57284cf5a466f23aad5979257d3045"
	realQBittorrentImage       = "lscr.io/linuxserver/qbittorrent:5.2.3@sha256:352371a7242e8b4aa10958ca02076d1023758070519b89a10251475fb9f1a35a"
)

// TestRealProviderQBittorrentPortForward is intentionally gated and assumes a
// release-manifest-pinned Waycloak installation plus an operator-provisioned
// credential Secret. It never reads or prints that Secret's data.
func TestRealProviderQBittorrentPortForward(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_REAL_PORT_FORWARD") != "1" {
		t.Skip("set WAYCLOAK_E2E_REAL_PORT_FORWARD=1 after installing immutable release images and provisioning rotated credentials")
	}
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize the selected non-Kind cluster")
	}
	namespace := requireRealVPNEnvironment(t, "WAYCLOAK_REAL_VPN_NAMESPACE")
	adapterImage := requireImmutableEnvironment(t, "WAYCLOAK_REAL_QBITTORRENT_ADAPTER_IMAGE")
	soak := realAcceptanceDuration(t, "WAYCLOAK_REAL_PORT_FORWARD_SOAK", 10*time.Minute, 10*time.Minute)
	rotationTimeout := realAcceptanceDuration(t, "WAYCLOAK_REAL_PORT_ROTATION_TIMEOUT", time.Hour, 10*time.Minute)

	observerBinary := filepath.Join(t.TempDir(), "qbittorrent-observer")
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"), "go", "build", "-trimpath", "-o", observerBinary, "../fixtures/qbittorrent-observer")
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	ctx := context.Background()
	nodeName := realPortForwardNode(t, direct)
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	prefix := "waycloak-real-pf-" + suffix

	gatewayName := strings.TrimSpace(os.Getenv("WAYCLOAK_REAL_VPN_GATEWAY"))
	ownedGateway := gatewayName == ""
	var gateway *wayv1.VPNGateway
	if ownedGateway {
		secretName := requireRealVPNEnvironment(t, "WAYCLOAK_REAL_VPN_SECRET")
		if !realCredentialSecretHasExpectedKeys(namespace, secretName) {
			t.Fatal("the configured credential Secret must exist and contain only username and password keys")
		}
		engineConfig := realPortForwardEngineConfig(namespace, prefix+"-engine")
		must(t, direct.Create(ctx, engineConfig))
		gateway = realPortForwardGateway(namespace, prefix+"-gateway", secretName, engineConfig.Name)
		must(t, direct.Create(ctx, gateway))
	} else {
		gateway = realPortForwardExistingGateway(t, ctx, direct, namespace, gatewayName)
	}
	t.Cleanup(func() {
		removeRealPortForwardResources(t, ctx, direct, namespace, prefix, gateway, ownedGateway)
	})
	waitFor(t, 10*time.Minute, func() bool {
		var current wayv1.VPNGateway
		if direct.Get(ctx, client.ObjectKeyFromObject(gateway), &current) != nil {
			return false
		}
		condition := apiMeta.FindStatusCondition(current.Status.Conditions, waystatus.ConditionReady)
		return condition != nil && condition.Status == metav1.ConditionTrue
	})

	plain := realPortForwardProbePod(prefix+"-plain", namespace, nodeName)
	must(t, direct.Create(ctx, plain))
	waitForPodReady(t, direct, plain)

	apiKey := randomAPIKey(t)
	auth := realQBittorrentAuthSecret(prefix+"-auth", namespace, apiKey)
	must(t, direct.Create(ctx, auth))
	leaseName := prefix + "-lease"
	adapterTrust := &wayv1.WorkloadAdapter{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-qbittorrent"}, Spec: wayv1.WorkloadAdapterSpec{ProtocolVersion: contract.AdapterProtocolVersion, Image: adapterImage}}
	must(t, direct.Create(ctx, adapterTrust))
	t.Cleanup(func() { _ = direct.Delete(context.Background(), adapterTrust) })
	protected := realQBittorrentPod(prefix+"-qbittorrent", namespace, nodeName, gateway.Name, auth.Name, adapterTrust.Name, adapterImage, leaseName, prefix)
	must(t, direct.Create(ctx, protected))
	waitFor(t, 3*time.Minute, func() bool {
		if direct.Get(ctx, client.ObjectKeyFromObject(protected), protected) != nil || protected.Status.PodIP == "" {
			return false
		}
		return containerRunning(protected, "qbittorrent") && containerRunning(protected, contract.AgentContainer) && containerRunning(protected, "acceptance-observer")
	})
	initialUID := protected.UID
	if podReadyCondition(protected) {
		t.Fatal("provider-assigned qBitTorrent Pod became Ready before its adapter received a lease")
	}
	assertRealQBittorrentIsolation(t, protected)
	copyLocalFile(t, observerBinary, namespace, protected.Name, "/tmp/qbittorrent-observer", "acceptance-observer")
	command(t, nil, "kubectl", "exec", "-n", namespace, protected.Name, "-c", "acceptance-observer", "--", "sh", "-ec", "chmod +x /tmp/qbittorrent-observer; nohup /tmp/qbittorrent-observer serve-tracker --output=/tmp/tracker-port >/tmp/tracker.log 2>&1 </dev/null &")

	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: leaseName, Namespace: namespace}, Spec: wayv1.PortForwardLeaseSpec{
		GatewayRef: wayv1.NamespacedNameReference{Name: gateway.Name},
		Target:     wayv1.PortForwardTargetSpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"acceptance.networking.waycloak.io/run": prefix}}, Port: 6881, ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned},
		Protocols:  []wayv1.PortForwardProtocol{wayv1.PortForwardProtocolTCP, wayv1.PortForwardProtocolUDP},
	}}
	must(t, direct.Create(ctx, lease))
	current := waitForRealLeaseReady(t, ctx, direct, lease, 10*time.Minute)
	initialPort := current.Status.PublicPort
	if initialPort < 1 || current.Status.IssuedAt == nil {
		t.Fatal("ready real-provider lease omitted public port or issue time")
	}
	initialIssuedAt := current.Status.IssuedAt.Time
	waitForPodReady(t, direct, protected)
	assertPodUID(t, ctx, direct, protected, initialUID)

	vpnIP := publicIPFromContainer(t, namespace, protected.Name, "qbittorrent")
	plainIP := publicIPFromContainer(t, namespace, plain.Name, "probe")
	if vpnIP == plainIP {
		t.Fatal("protected and ordinary probes observed the same public egress address")
	}
	probeRealPort(t, namespace, plain.Name, vpnIP, uint16(initialPort), true)
	addAcceptanceMagnet(t, namespace, protected.Name, suffix)
	waitForTrackerPort(t, namespace, protected.Name, uint16(initialPort), 2*time.Minute)
	waitForDHTNodes(t, namespace, protected.Name, 10*time.Minute)

	renewed := false
	rotated := false
	deadline := time.Now().Add(rotationTimeout)
	soakDeadline := time.Now().Add(soak)
	for time.Now().Before(deadline) || time.Now().Before(soakDeadline) {
		current = waitForRealLeaseReady(t, ctx, direct, lease, 2*time.Minute)
		assertPodUID(t, ctx, direct, protected, initialUID)
		if current.Status.IssuedAt != nil && current.Status.IssuedAt.After(initialIssuedAt) {
			renewed = true
		}
		if current.Status.PublicPort != initialPort {
			rotated = true
			vpnIP = publicIPFromContainer(t, namespace, protected.Name, "qbittorrent")
			probeRealPort(t, namespace, plain.Name, vpnIP, uint16(current.Status.PublicPort), true)
			waitForTrackerPort(t, namespace, protected.Name, uint16(current.Status.PublicPort), 2*time.Minute)
		}
		if dhtNodes(namespace, protected.Name) < 1 {
			t.Fatal("qBitTorrent DHT became unhealthy during real-provider soak")
		}
		if renewed && rotated && !time.Now().Before(soakDeadline) {
			break
		}
		time.Sleep(30 * time.Second)
	}
	if !renewed {
		t.Fatal("no real NAT-PMP renewal was observed")
	}
	if !rotated {
		t.Fatal("no real provider public-port rotation was observed before the configured timeout")
	}

	oldPort := uint16(current.Status.PublicPort)
	oldVPNIP := vpnIP
	gatewayPod := servingGatewayPod(t, ctx, direct, gateway)
	must(t, direct.Delete(ctx, gatewayPod, client.GracePeriodSeconds(0)))
	waitFor(t, 2*time.Minute, func() bool {
		var item wayv1.PortForwardLease
		if direct.Get(ctx, client.ObjectKeyFromObject(lease), &item) != nil {
			return false
		}
		condition := apiMeta.FindStatusCondition(item.Status.Conditions, waystatus.ConditionReady)
		return condition != nil && condition.Status == metav1.ConditionFalse
	})
	if exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "qbittorrent", "--", "wget", "-qO-", "-T", "5", "https://api.ipify.org").Run() == nil {
		t.Fatal("protected qBitTorrent egress did not fail closed after gateway loss")
	}
	probeRealPort(t, namespace, plain.Name, oldVPNIP, oldPort, false)
	current = waitForRealLeaseReady(t, ctx, direct, lease, 10*time.Minute)
	waitForPodReady(t, direct, protected)
	assertPodUID(t, ctx, direct, protected, initialUID)
	vpnIP = publicIPFromContainer(t, namespace, protected.Name, "qbittorrent")
	probeRealPort(t, namespace, plain.Name, vpnIP, uint16(current.Status.PublicPort), true)
	waitForTrackerPort(t, namespace, protected.Name, uint16(current.Status.PublicPort), 2*time.Minute)
	waitForDHTNodes(t, namespace, protected.Name, 5*time.Minute)
}

func realPortForwardExistingGateway(t *testing.T, ctx context.Context, c client.Client, namespace, name string) *wayv1.VPNGateway {
	t.Helper()
	var gateway wayv1.VPNGateway
	must(t, c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &gateway))
	ready := apiMeta.FindStatusCondition(gateway.Status.Conditions, waystatus.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("configured existing gateway %s/%s is not Ready", namespace, name)
	}
	if gateway.Spec.Engine.Image != realPortForwardEngineImage {
		t.Fatalf("configured existing gateway %s/%s does not use the tested Gluetun image", namespace, name)
	}
	if !gateway.Spec.PortForwarding.Enabled || gateway.Spec.PortForwarding.Driver != "ProtonNatPmp" {
		t.Fatalf("configured existing gateway %s/%s does not provide Proton NAT-PMP port forwarding", namespace, name)
	}
	return &gateway
}

func realPortForwardNode(t *testing.T, c client.Client) string {
	t.Helper()
	configured := strings.TrimSpace(os.Getenv("WAYCLOAK_REAL_VPN_NODE"))
	if configured == "" {
		return amd64Node(t, c)
	}
	var node corev1.Node
	must(t, c.Get(context.Background(), client.ObjectKey{Name: configured}, &node))
	if node.Labels[corev1.LabelArchStable] != "amd64" {
		t.Fatalf("configured real-provider node %q is not amd64", configured)
	}
	if node.Spec.Unschedulable {
		t.Fatalf("configured real-provider node %q is unschedulable", configured)
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return node.Name
		}
	}
	t.Fatalf("configured real-provider node %q is not Ready", configured)
	return ""
}

func TestRealPortForwardNodeOverride(t *testing.T) {
	t.Setenv("WAYCLOAK_REAL_VPN_NODE", "reviewed-worker")
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "reviewed-worker", Labels: map[string]string{corev1.LabelArchStable: "amd64"}},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: corev1.ConditionTrue,
		}}},
	}
	direct := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
	if got := realPortForwardNode(t, direct); got != node.Name {
		t.Fatalf("real provider node = %q, want %q", got, node.Name)
	}
}

func TestRealPortForwardExistingGateway(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, wayv1.AddToScheme(scheme))
	gateway := &wayv1.VPNGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "reviewed-gateway", Namespace: "acceptance"},
		Spec: wayv1.VPNGatewaySpec{
			Engine:         wayv1.EngineSpec{Type: "Gluetun", Image: realPortForwardEngineImage},
			PortForwarding: wayv1.PortForwardingSpec{Enabled: true, Driver: "ProtonNatPmp"},
		},
		Status: wayv1.VPNGatewayStatus{Conditions: []metav1.Condition{{
			Type: waystatus.ConditionReady, Status: metav1.ConditionTrue,
		}}},
	}
	direct := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway).Build()
	got := realPortForwardExistingGateway(t, context.Background(), direct, gateway.Namespace, gateway.Name)
	if got.UID != gateway.UID || got.Name != gateway.Name {
		t.Fatalf("existing gateway = %s/%s, want %s/%s", got.Namespace, got.Name, gateway.Namespace, gateway.Name)
	}
}

func TestRealQBittorrentReadinessUsesLoopback(t *testing.T) {
	pod := realQBittorrentPod("qbittorrent", "acceptance", "worker", "gateway", "auth", "adapter", "example.invalid/adapter:1@sha256:"+strings.Repeat("a", 64), "lease", "run")
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		if container.Name != "qbittorrent" {
			continue
		}
		if container.ReadinessProbe == nil || container.ReadinessProbe.Exec == nil {
			t.Fatal("qBitTorrent readiness must execute inside the shared network namespace")
		}
		command := strings.Join(container.ReadinessProbe.Exec.Command, " ")
		if !strings.Contains(command, "http://127.0.0.1:8080/") {
			t.Fatalf("qBitTorrent readiness command does not use the loopback WebUI: %q", command)
		}
		return
	}
	t.Fatal("qBitTorrent container is missing")
}

func TestRealQBittorrentConfigurationEnablesDHT(t *testing.T) {
	secret := realQBittorrentAuthSecret("auth", "acceptance", "test-key")
	configuration := secret.StringData["qBittorrent.conf"]
	if !strings.Contains(configuration, "Session\\DHTEnabled=true") {
		t.Fatal("real-provider qBitTorrent configuration must explicitly enable DHT")
	}
}

func TestRandomAPIKeyMatchesQBittorrentFormat(t *testing.T) {
	key := randomAPIKey(t)
	if len(key) != 32 || !strings.HasPrefix(key, "qbt_") {
		t.Fatalf("qBitTorrent API key has invalid format: length=%d", len(key))
	}
}

func realPortForwardGateway(namespace, name, secretName, engineConfigName string) *wayv1.VPNGateway {
	return &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: wayv1.VPNGatewaySpec{
		Engine: wayv1.EngineSpec{Type: "Gluetun", Image: realPortForwardEngineImage, Config: &wayv1.EngineNativeConfigSpec{
			EnvFrom: []corev1.LocalObjectReference{{Name: engineConfigName}},
			Files:   []wayv1.EngineFileSource{{SecretRef: &corev1.LocalObjectReference{Name: secretName}, MountPath: "/run/engine-native/credentials"}},
		}},
		Overlay:        wayv1.OverlaySpec{CIDR: "172.30.252.0/29", VNI: 10992, MTU: 1320},
		ClusterTraffic: wayv1.ClusterTrafficSpec{Mode: "Gateway"},
		PortForwarding: wayv1.PortForwardingSpec{Enabled: true, Driver: "ProtonNatPmp"},
		WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: metav1.LabelSelector{}},
	}}
}

func realPortForwardEngineConfig(namespace, name string) *corev1.ConfigMap {
	region := strings.TrimSpace(os.Getenv("WAYCLOAK_REAL_VPN_REGION"))
	if region == "" {
		region = "Switzerland"
	}
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Data: map[string]string{
		"VPN_SERVICE_PROVIDER":        "protonvpn",
		"VPN_TYPE":                    "openvpn",
		"SERVER_COUNTRIES":            region,
		"OPENVPN_USER_SECRETFILE":     "/run/engine-native/credentials/username",
		"OPENVPN_PASSWORD_SECRETFILE": "/run/engine-native/credentials/password",
	}}
}

func TestRealPortForwardGatewayUsesNativeGluetunConfiguration(t *testing.T) {
	gateway := realPortForwardGateway("acceptance", "gateway", "credentials", "engine-config")
	if gateway.Spec.Provider != nil {
		t.Fatal("real-provider gateway retained the legacy provider configuration")
	}
	if gateway.Spec.Engine.Config == nil || len(gateway.Spec.Engine.Config.EnvFrom) != 1 || gateway.Spec.Engine.Config.EnvFrom[0].Name != "engine-config" {
		t.Fatal("real-provider gateway omitted its native Gluetun environment reference")
	}
	if len(gateway.Spec.Engine.Config.Files) != 1 || gateway.Spec.Engine.Config.Files[0].SecretRef == nil || gateway.Spec.Engine.Config.Files[0].SecretRef.Name != "credentials" {
		t.Fatal("real-provider gateway omitted its engine-only credential mount")
	}
	config := realPortForwardEngineConfig("acceptance", "engine-config")
	if config.Data["VPN_SERVICE_PROVIDER"] != "protonvpn" || config.Data["VPN_TYPE"] != "openvpn" {
		t.Fatal("real-provider native Gluetun configuration is not compatible with Proton NAT-PMP")
	}
}

func realCredentialSecretHasExpectedKeys(namespace, name string) bool {
	template := `{{range $key, $_ := .data}}{{$key}}{{"\n"}}{{end}}`
	output, err := exec.Command("kubectl", "get", "secret", "-n", namespace, name, "-o", "go-template="+template).CombinedOutput()
	if err != nil {
		return false
	}
	keys := strings.Fields(string(output))
	sort.Strings(keys)
	return len(keys) == 2 && keys[0] == "password" && keys[1] == "username"
}

func realQBittorrentAuthSecret(name, namespace, apiKey string) *corev1.Secret {
	configuration := "[BitTorrent]\nSession\\DHTEnabled=true\nSession\\Port=6881\nSession\\UseRandomPort=false\n\n[LegalNotice]\nAccepted=true\n\n[Network]\nPortForwardingEnabled=false\n\n[Preferences]\nConnection\\PortRangeMin=6881\nConnection\\UPnP=false\nWebUI\\Address=127.0.0.1\nWebUI\\APIKey=" + apiKey + "\nWebUI\\Port=8080\n"
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, StringData: map[string]string{"api-key": apiKey, "qBittorrent.conf": configuration}}
}

func realQBittorrentPod(name, namespace, node, gateway, auth, adapterTrust, adapterImage, leaseName, run string) *corev1.Pod {
	no := false
	yes := true
	runAs := int64(65532)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"acceptance.networking.waycloak.io/run": run}, Annotations: map[string]string{contract.GatewayAnnotation: namespace + "/" + gateway, contract.WorkloadAdapterAnnotation: adapterTrust, contract.AdapterContainerAnnotation: "waycloak-qbittorrent-adapter"}}, Spec: corev1.PodSpec{
		NodeName: node, AutomountServiceAccountToken: &no,
		InitContainers: []corev1.Container{{Name: "configure", Image: "alpine:3.22.1", Command: []string{"sh", "-ec"}, Args: []string{"mkdir -p /config/qBittorrent; cp /bootstrap/qBittorrent.conf /config/qBittorrent/qBittorrent.conf; chown -R 1000:1000 /config"}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config"}, {Name: "bootstrap", MountPath: "/bootstrap", ReadOnly: true}}}},
		Containers: []corev1.Container{
			{Name: "qbittorrent", Image: realQBittorrentImage, Env: []corev1.EnvVar{{Name: "PUID", Value: "1000"}, {Name: "PGID", Value: "1000"}, {Name: "TZ", Value: "Etc/UTC"}}, Ports: []corev1.ContainerPort{{Name: "bittorrent-tcp", ContainerPort: 6881, Protocol: corev1.ProtocolTCP}, {Name: "bittorrent-udp", ContainerPort: 6881, Protocol: corev1.ProtocolUDP}, {Name: "webui", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-ec", "wget -qO- -T 2 http://127.0.0.1:8080/ >/dev/null"}}}, PeriodSeconds: 5}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config"}, {Name: "downloads", MountPath: "/downloads"}}},
			{Name: "waycloak-qbittorrent-adapter", Image: adapterImage, Args: []string{"run"}, Env: []corev1.EnvVar{{Name: "WAYCLOAK_QBITTORRENT_API_KEY_FILE", Value: "/adapter-auth/api-key"}, {Name: "WAYCLOAK_LEASE_NAME", Value: leaseName}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"/ko-app/qbittorrent-adapter", "probe"}}}, PeriodSeconds: 2}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, RunAsNonRoot: &yes, RunAsUser: &runAs, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, VolumeMounts: []corev1.VolumeMount{{Name: "adapter-auth", MountPath: "/adapter-auth", ReadOnly: true}}},
			{Name: "acceptance-observer", Image: "alpine:3.22.1", Command: []string{"sleep", "86400"}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "observer", MountPath: "/tmp"}, {Name: "adapter-auth", MountPath: "/secrets", ReadOnly: true}}},
		},
		Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "downloads", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "observer", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "bootstrap", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: auth, Items: []corev1.KeyToPath{{Key: "qBittorrent.conf", Path: "qBittorrent.conf"}}}}}, {Name: "adapter-auth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: auth, Items: []corev1.KeyToPath{{Key: "api-key", Path: "api-key"}}}}}},
	}}
}

func realPortForwardProbePod(name, namespace, node string) *corev1.Pod {
	no := false
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: corev1.PodSpec{NodeName: node, AutomountServiceAccountToken: &no, Containers: []corev1.Container{{Name: "probe", Image: "python:3.13-alpine", Command: []string{"python", "-c", "import time; time.sleep(86400)"}}}}}
}

func waitForRealLeaseReady(t *testing.T, ctx context.Context, c client.Client, lease *wayv1.PortForwardLease, timeout time.Duration) wayv1.PortForwardLease {
	t.Helper()
	var current wayv1.PortForwardLease
	waitFor(t, timeout, func() bool {
		if c.Get(ctx, client.ObjectKeyFromObject(lease), &current) != nil {
			return false
		}
		condition := apiMeta.FindStatusCondition(current.Status.Conditions, waystatus.ConditionReady)
		return condition != nil && condition.Status == metav1.ConditionTrue && current.Status.PublicPort > 0
	})
	return current
}

func servingGatewayPod(t *testing.T, ctx context.Context, c client.Client, gateway *wayv1.VPNGateway) *corev1.Pod {
	t.Helper()
	var pods corev1.PodList
	must(t, c.List(ctx, &pods, client.InNamespace(gateway.Namespace), client.MatchingLabels(waygateway.SelectorLabels(gateway))))
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp.IsZero() && pods.Items[i].Status.PodIP != "" {
			return &pods.Items[i]
		}
	}
	t.Fatal("serving gateway Pod is unavailable")
	return nil
}

func addAcceptanceMagnet(t *testing.T, namespace, pod, suffix string) {
	t.Helper()
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=waycloak-real-" + suffix + "&tr=http%3A%2F%2F127.0.0.1%3A18081%2Fannounce"
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "-c", "qbittorrent", "--", "su", "-s", "/bin/sh", "abc", "-c", "/app/qbittorrent-nox '"+magnet+"'")
}

func waitForTrackerPort(t *testing.T, namespace, pod string, port uint16, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() bool {
		return commandSucceedsContainer(namespace, pod, "acceptance-observer", fmt.Sprintf("test \"$(cat /tmp/tracker-port 2>/dev/null)\" = %d", port))
	})
}

func waitForDHTNodes(t *testing.T, namespace, pod string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() bool { return dhtNodes(namespace, pod) > 0 })
}

func dhtNodes(namespace, pod string) int {
	output, err := exec.Command("kubectl", "exec", "-n", namespace, pod, "-c", "acceptance-observer", "--", "/tmp/qbittorrent-observer", "dht-nodes").CombinedOutput()
	if err != nil {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0
	}
	return value
}

func probeRealPort(t *testing.T, namespace, pod string, address netip.Addr, port uint16, expectSuccess bool) {
	t.Helper()
	tcpProgram := `import socket,sys
host,port=sys.stdin.read().split()
port=int(port)
with socket.create_connection((host,port),timeout=10): pass
`
	udpProgram := `import socket,sys
host,port=sys.stdin.read().split()
port=int(port)
payload=b'd1:ad2:id20:abcdefghij0123456789e1:q4:ping1:t2:wc1:y1:qe'
with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as connection:
    connection.settimeout(10)
    connection.sendto(payload,(host,port))
    response,_=connection.recvfrom(2048)
    assert b'1:t2:wc' in response and b'1:y1:r' in response
`
	probeRealTransport(t, namespace, pod, address, port, "TCP", tcpProgram, expectSuccess)
	probeRealTransport(t, namespace, pod, address, port, "UDP DHT", udpProgram, expectSuccess)
}

func probeRealTransport(t *testing.T, namespace, pod string, address netip.Addr, port uint16, transport, program string, expectSuccess bool) {
	t.Helper()
	cmd := exec.Command("kubectl", "exec", "-i", "-n", namespace, pod, "-c", "probe", "--", "python", "-c", program)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s %d", address, port))
	_, err := cmd.CombinedOutput()
	if expectSuccess && err != nil {
		t.Fatalf("real %s probe failed: %v", transport, err)
	}
	if !expectSuccess && err == nil {
		t.Fatalf("stale real %s endpoint remained reachable", transport)
	}
}

func publicIPFromContainer(t *testing.T, namespace, pod, container string) netip.Addr {
	t.Helper()
	value := strings.TrimSpace(command(t, nil, "kubectl", "exec", "-n", namespace, pod, "-c", container, "--", "wget", "-qO-", "-T", "30", "https://api.ipify.org"))
	address, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatal("public egress endpoint did not return a valid IP address")
	}
	return address
}

func assertPodUID(t *testing.T, ctx context.Context, c client.Client, pod *corev1.Pod, uid types.UID) {
	t.Helper()
	var current corev1.Pod
	must(t, c.Get(ctx, client.ObjectKeyFromObject(pod), &current))
	if current.UID != uid {
		t.Fatal("provider renewal or rotation replaced the protected Pod")
	}
}

func assertRealQBittorrentIsolation(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("protected qBitTorrent Pod received a Kubernetes API token")
	}
	for _, container := range pod.Spec.Containers {
		if container.Name == contract.AgentContainer {
			continue
		}
		if container.SecurityContext != nil && container.SecurityContext.Capabilities != nil && len(container.SecurityContext.Capabilities.Add) != 0 {
			t.Fatalf("application-side container %s received added capabilities", container.Name)
		}
	}
}

func removeRealPortForwardResources(t *testing.T, ctx context.Context, c client.Client, namespace, prefix string, gateway *wayv1.VPNGateway, ownedGateway bool) {
	t.Helper()
	var leases wayv1.PortForwardLeaseList
	if c.List(ctx, &leases, client.InNamespace(namespace)) == nil {
		for i := range leases.Items {
			if strings.HasPrefix(leases.Items[i].Name, prefix) && leases.Items[i].DeletionTimestamp.IsZero() {
				_ = c.Delete(ctx, &leases.Items[i])
			}
		}
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		remaining := false
		if c.List(ctx, &leases, client.InNamespace(namespace)) == nil {
			for i := range leases.Items {
				remaining = remaining || strings.HasPrefix(leases.Items[i].Name, prefix)
			}
		}
		if !remaining {
			break
		}
		time.Sleep(time.Second)
	}
	if c.List(ctx, &leases, client.InNamespace(namespace)) == nil {
		for i := range leases.Items {
			if !strings.HasPrefix(leases.Items[i].Name, prefix) {
				continue
			}
			finalizers := leases.Items[i].Finalizers[:0]
			for _, finalizer := range leases.Items[i].Finalizers {
				if finalizer != contract.PortForwardLeaseFinalizer {
					finalizers = append(finalizers, finalizer)
				}
			}
			leases.Items[i].Finalizers = finalizers
			if err := c.Update(ctx, &leases.Items[i]); err != nil {
				t.Logf("remove stuck acceptance lease finalizer: %v", err)
			} else {
				_ = c.Delete(ctx, &leases.Items[i])
			}
		}
	}
	for _, name := range []string{prefix + "-qbittorrent", prefix + "-plain"} {
		_ = c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}, client.GracePeriodSeconds(0))
	}
	_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-auth", Namespace: namespace}})
	_ = c.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-engine", Namespace: namespace}})
	if ownedGateway {
		_ = c.Delete(ctx, gateway)
	}
}

func randomAPIKey(t *testing.T) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	value := make([]byte, 28)
	limit := big.NewInt(int64(len(alphabet)))
	for index := range value {
		selected, err := rand.Int(rand.Reader, limit)
		if err != nil {
			t.Fatal(err)
		}
		value[index] = alphabet[selected.Int64()]
	}
	return "qbt_" + string(value)
}

func requireImmutableEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := requireRealVPNEnvironment(t, name)
	parts := strings.Split(value, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		t.Fatalf("%s must be an immutable sha256 digest reference", name)
	}
	return value
}

func realAcceptanceDuration(t *testing.T, name string, fallback, minimum time.Duration) time.Duration {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < minimum {
		t.Fatalf("%s must be a duration of at least %s", name, minimum)
	}
	return duration
}
