// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	wayadmission "github.com/Amoenus/waycloak/internal/admission"
	"github.com/Amoenus/waycloak/internal/contract"
	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const realVPNEngineImage = "docker.io/qmcgaw/gluetun:v3.41.0@sha256:6b54856716d0de56e5bb00a77029b0adea57284cf5a466f23aad5979257d3045"

// TestRealVPNProtectedPath is deliberately opt-in: the operator provisions a
// username/password Secret and grants use of the selected cluster explicitly.
// The test references that Secret but never reads or prints its data.
func TestRealVPNProtectedPath(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_REAL_VPN") != "1" {
		t.Skip("set WAYCLOAK_E2E_REAL_VPN=1 with an existing credential Secret to run real-provider acceptance")
	}
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	namespace := requireRealVPNEnvironment(t, "WAYCLOAK_REAL_VPN_NAMESPACE")
	secretName := requireRealVPNEnvironment(t, "WAYCLOAK_REAL_VPN_SECRET")
	if exec.Command("kubectl", "get", "secret", "-n", namespace, secretName, "-o", "name").Run() != nil {
		t.Fatal("the configured credential Secret does not exist")
	}

	managerBinary := filepath.Join(t.TempDir(), "gateway-manager")
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"), "go", "build", "-trimpath", "-o", managerBinary, "../../cmd/gateway-manager")
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	imageTar, imageTag := buildAgentTarball(t, "real-"+suffix)
	command(t, nil, "kubectl", "apply", "-f", filepath.Join("..", "..", "config", "crd", "bases"))

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	ctx := context.Background()
	nodeName := amd64Node(t, direct)
	prefix := "waycloak-real-" + suffix

	loader := imageLoaderPod(namespace, nodeName)
	loader.Name = prefix + "-loader"
	must(t, direct.Create(ctx, loader))
	t.Cleanup(func() { _ = direct.Delete(ctx, loader, client.GracePeriodSeconds(0)) })
	waitForPodReady(t, direct, loader)
	copyLocalFile(t, imageTar, namespace, loader.Name, "/tmp/agent.tar")
	archive := prefix + "-agent.tar"
	command(t, nil, "kubectl", "exec", "-n", namespace, loader.Name, "--", "cp", "/tmp/agent.tar", "/host/images/"+archive)
	waitFor(t, 60*time.Second, func() bool {
		output, listErr := exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "/host/k3s", "ctr", "--address", "/host/containerd/containerd.sock", "--namespace", "k8s.io", "images", "ls", "-q").CombinedOutput()
		return listErr == nil && strings.Contains(string(output), imageTag)
	})
	imageDigest := importedImageDigest(t, namespace, loader.Name, imageTag)
	agentImage := imageTag + "@" + imageDigest
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "rm", "-f", "/host/images/"+archive).Run()
		_ = exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "/host/k3s", "ctr", "--address", "/host/containerd/containerd.sock", "--namespace", "k8s.io", "images", "rm", imageTag, "waycloak.test/agent@"+imageDigest).Run()
	})

	gateway := realVPNGateway(namespace, prefix, secretName)
	must(t, direct.Create(ctx, gateway))
	t.Cleanup(func() { _ = direct.Delete(ctx, gateway) })

	plain := realVPNApplicationPod(prefix+"-plain", namespace, nodeName)
	must(t, direct.Create(ctx, plain))
	t.Cleanup(func() { _ = direct.Delete(ctx, plain, client.GracePeriodSeconds(0)) })
	waitForPodReady(t, direct, plain)

	protected := realVPNApplicationPod(prefix+"-protected", namespace, nodeName)
	protected.Annotations = map[string]string{contract.GatewayAnnotation: namespace + "/" + gateway.Name}
	mutator := wayadmission.PodMutator{Client: direct, AgentImage: agentImage}
	changed, err := mutator.Mutate(ctx, protected)
	must(t, err)
	if !changed {
		t.Fatal("annotated Pod was not injected")
	}
	assertRealVPNApplicationIsolation(t, protected)
	must(t, direct.Create(ctx, protected))
	t.Cleanup(func() { _ = direct.Delete(ctx, protected, client.GracePeriodSeconds(0)) })
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(protected), protected))
	if applicationRunning(protected) {
		t.Fatal("application started before its required allocation ConfigMap existed")
	}

	memberAddress := "172.30.251.2"
	gatewayConfig := waygateway.DesiredConfigMap(gateway, nil)
	must(t, direct.Create(ctx, gatewayConfig))
	t.Cleanup(func() { _ = direct.Delete(ctx, gatewayConfig) })

	gatewayPod := realVPNGatewayPod(gateway, prefix+"-gateway", nodeName)
	must(t, direct.Create(ctx, gatewayPod))
	t.Cleanup(func() { _ = direct.Delete(ctx, gatewayPod, client.GracePeriodSeconds(0)) })
	waitFor(t, 120*time.Second, func() bool {
		return direct.Get(ctx, client.ObjectKeyFromObject(gatewayPod), gatewayPod) == nil && gatewayPod.Status.PodIP != "" && containerRunning(gatewayPod, waygateway.ManagerContainer)
	})
	copyToContainer(t, managerBinary, namespace, gatewayPod.Name, waygateway.ManagerContainer, "/tmp/gateway-manager")
	command(t, nil, "kubectl", "exec", "-n", namespace, gatewayPod.Name, "-c", waygateway.ManagerContainer, "--", "sh", "-c", "nohup /tmp/gateway-manager run --engine-type=Gluetun --config-path=/run/waycloak/config/gateway.json --resolv-conf=/run/waycloak/runtime/resolv.conf >/run/waycloak/runtime/manager.log 2>&1 &")

	allocationName := protected.Annotations[contract.AllocationNameAnnotation]
	allocation := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      allocationName,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Pod", Name: protected.Name, UID: protected.UID, Controller: boolPtr(true), BlockOwnerDeletion: boolPtr(true),
			}},
		},
		Data: map[string]string{
			"version": contract.InjectionVersion, "podUID": string(protected.UID), "gateway": namespace + "/" + gateway.Name,
			"address": memberAddress, "overlayCIDR": gateway.Spec.Overlay.CIDR, "gatewayAddress": "172.30.251.1",
			"gatewayEndpoint": gatewayPod.Status.PodIP + ":4789", "gatewayHealthPort": fmt.Sprint(waygateway.HealthPort),
			"vni": fmt.Sprint(gateway.Spec.Overlay.VNI), "mtu": fmt.Sprint(gateway.Spec.Overlay.MTU),
			"clusterTrafficMode": "Gateway", "allocationGeneration": "1",
		},
	}
	must(t, direct.Create(ctx, allocation))
	t.Cleanup(func() { _ = direct.Delete(ctx, allocation) })
	waitFor(t, 60*time.Second, func() bool {
		return direct.Get(ctx, client.ObjectKeyFromObject(protected), protected) == nil && protected.Status.PodIP != ""
	})
	desiredWithMember := waygateway.DesiredConfigMap(gateway, []waygateway.Member{{ID: string(protected.UID), OverlayAddress: memberAddress, UnderlayIP: protected.Status.PodIP}})
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(gatewayConfig), gatewayConfig))
	gatewayConfig.Data = desiredWithMember.Data
	must(t, direct.Update(ctx, gatewayConfig))
	waitFor(t, 300*time.Second, func() bool {
		return direct.Get(ctx, client.ObjectKeyFromObject(gatewayPod), gatewayPod) == nil && containerReadyByName(gatewayPod, waygateway.ManagerContainer)
	})
	waitFor(t, 180*time.Second, func() bool { return podReady(t, direct, protected) })
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(protected), protected))
	assertRealVPNApplicationIsolation(t, protected)

	plainIP := publicIPFromPod(t, namespace, plain.Name)
	vpnIP := publicIPFromPod(t, namespace, protected.Name)
	if plainIP == vpnIP {
		t.Fatal("protected and ordinary workloads observed the same public egress address")
	}
	fqdnDNS := exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "getent", "hosts", "kubernetes.default.svc.cluster.local").Run() == nil
	searchDNS := exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "getent", "hosts", "kubernetes.default").Run() == nil
	if !fqdnDNS || !searchDNS {
		t.Fatalf("protected cluster DNS failed (fully-qualified=%t search-domain=%t)", fqdnDNS, searchDNS)
	}
	command(t, nil, "kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "nc", "-z", "-w", "10", "1.1.1.1", "443")

	must(t, direct.Delete(ctx, gatewayPod, client.GracePeriodSeconds(0)))
	waitFor(t, 60*time.Second, func() bool { return !podReady(t, direct, protected) })
	if exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "nc", "-z", "-w", "5", "1.1.1.1", "443").Run() == nil {
		t.Fatal("protected application reached a direct IP after gateway loss")
	}
	if exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "getent", "hosts", "kubernetes.default").Run() == nil {
		t.Fatal("protected application resolved DNS after gateway loss")
	}
	_ = publicIPFromPod(t, namespace, plain.Name)
}

func requireRealVPNEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when WAYCLOAK_E2E_REAL_VPN=1", name)
	}
	return value
}

func realVPNGateway(namespace, name, secretName string) *wayv1.VPNGateway {
	return &wayv1.VPNGateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: wayv1.VPNGatewaySpec{
			Engine:         wayv1.EngineSpec{Type: "Gluetun", Image: realVPNEngineImage},
			Provider:       wayv1.ProviderSpec{Name: "protonvpn", Protocol: "OpenVPN", Region: "Netherlands", CredentialsSecretRef: corev1.LocalObjectReference{Name: secretName}},
			Overlay:        wayv1.OverlaySpec{CIDR: "172.30.251.0/29", VNI: 10991, MTU: 1320},
			ClusterTraffic: wayv1.ClusterTrafficSpec{Mode: "Gateway"},
			WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: metav1.LabelSelector{}},
		},
	}
}

func realVPNApplicationPod(name, namespace, node string) *corev1.Pod {
	no := false
	yes := true
	runAs := int64(65532)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: corev1.PodSpec{
		NodeName: node, AutomountServiceAccountToken: &no,
		Containers: []corev1.Container{{Name: "app", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, RunAsNonRoot: &yes, RunAsUser: &runAs, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}}},
	}}
}

func realVPNGatewayPod(gateway *wayv1.VPNGateway, name, node string) *corev1.Pod {
	statefulSet := waygateway.DesiredStatefulSet(gateway, waygateway.WorkloadOptions{ManagerImage: "waycloak.invalid/gateway-manager@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	spec := statefulSet.Spec.Template.Spec
	spec.NodeName = node
	for i := range spec.InitContainers {
		if spec.InitContainers[i].Name == waygateway.FirewallRendererContainer {
			spec.InitContainers[i].Image = "alpine:3.22.1"
			spec.InitContainers[i].Command = []string{"sh", "-c"}
			spec.InitContainers[i].Args = []string{"set -eu; cp /run/waycloak/engine-firewall-base/post-rules.txt /run/waycloak/engine-firewall/post-rules.txt; cp /etc/resolv.conf /run/waycloak/runtime/resolv.conf; dns=$(awk '$1 == \"nameserver\" { print $2; exit }' /etc/resolv.conf); printf 'iptables --append OUTPUT --destination %s/32 --protocol udp --destination-port 53 --jump ACCEPT\\niptables --append OUTPUT --destination %s/32 --protocol tcp --destination-port 53 --jump ACCEPT\\n' \"$dns\" \"$dns\" >>/run/waycloak/engine-firewall/post-rules.txt"}
		}
	}
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: "test-binaries", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	for i := range spec.Containers {
		if spec.Containers[i].Name == waygateway.ManagerContainer {
			spec.Containers[i].Image = "alpine:3.22.1"
			spec.Containers[i].Command = []string{"sleep", "3600"}
			spec.Containers[i].Args = nil
			spec.Containers[i].VolumeMounts = append(spec.Containers[i].VolumeMounts, corev1.VolumeMount{Name: "test-binaries", MountPath: "/tmp"})
		}
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateway.Namespace, Labels: statefulSet.Spec.Template.Labels, Annotations: statefulSet.Spec.Template.Annotations}, Spec: spec}
}

func assertRealVPNApplicationIsolation(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("protected application received a Kubernetes API token")
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Secret != nil {
			t.Fatal("protected application received a Secret volume")
		}
	}
	app := pod.Spec.Containers[0]
	if app.SecurityContext == nil || app.SecurityContext.Capabilities == nil || len(app.SecurityContext.Capabilities.Add) != 0 {
		t.Fatal("application container received added Linux capabilities")
	}
}

func applicationRunning(pod *corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "app" && status.State.Running != nil {
			return true
		}
	}
	return false
}

func containerRunning(pod *corev1.Pod, name string) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == name && status.State.Running != nil {
			return true
		}
	}
	return false
}

func containerReadyByName(pod *corev1.Pod, name string) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == name {
			return status.Ready
		}
	}
	return false
}

func copyToContainer(t *testing.T, local, namespace, pod, container, remote string) {
	t.Helper()
	workingDirectory, err := os.Getwd()
	must(t, err)
	relative, err := filepath.Rel(workingDirectory, local)
	must(t, err)
	command(t, nil, "kubectl", "cp", "-c", container, relative, namespace+"/"+pod+":"+remote)
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "-c", container, "--", "chmod", "+x", remote)
}

func publicIPFromPod(t *testing.T, namespace, pod string) netip.Addr {
	t.Helper()
	value := strings.TrimSpace(command(t, nil, "kubectl", "exec", "-n", namespace, pod, "-c", "app", "--", "wget", "-qO-", "-T", "30", "https://api.ipify.org"))
	address, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatal("public egress endpoint did not return a valid IP address")
	}
	return address
}
