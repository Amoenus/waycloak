// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestFailClosedLockdownInPodNetworkNamespace(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	binary := filepath.Join(t.TempDir(), "dataplane.test")
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"), "go", "test", "-c", "-tags=e2e", "-o", binary, "../../internal/dataplane")
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	ctx := context.Background()
	namespace := fmt.Sprintf("waycloak-netns-e2e-%d", time.Now().UnixNano())
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	must(t, direct.Create(ctx, ns))
	t.Cleanup(func() { _ = direct.Delete(ctx, ns) })

	falseValue := false
	trueValue := true
	runAsRoot := int64(0)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: namespace}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: &falseValue,
		NodeSelector:                 map[string]string{"kubernetes.io/arch": "amd64"},
		Volumes:                      []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		Containers: []corev1.Container{{Name: "runner", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}, SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &falseValue, RunAsNonRoot: &falseValue, RunAsUser: &runAsRoot,
			Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"NET_ADMIN"}},
			ReadOnlyRootFilesystem: &trueValue,
		}}},
	}}
	must(t, direct.Create(ctx, pod))
	waitFor(t, 60*time.Second, func() bool {
		var current corev1.Pod
		if direct.Get(ctx, client.ObjectKeyFromObject(pod), &current) != nil {
			return false
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
	workingDirectory, err := os.Getwd()
	must(t, err)
	relativeBinary, err := filepath.Rel(workingDirectory, binary)
	must(t, err)
	command(t, nil, "kubectl", "cp", relativeBinary, namespace+"/runner:/tmp/dataplane.test")
	command(t, nil, "kubectl", "exec", "-n", namespace, "runner", "--", "chmod", "+x", "/tmp/dataplane.test")
	command(t, nil, "kubectl", "exec", "-n", namespace, "runner", "--", "env", "WAYCLOAK_E2E_NETNS=1", "/tmp/dataplane.test", "-test.run", "^TestLockdownDropsDirectPackets$", "-test.v")
}

func TestApplicationPortRedirectRotatesAtomically(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	binary := filepath.Join(t.TempDir(), "dataplane.test")
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"), "go", "test", "-c", "-tags=e2e", "-o", binary, "../../internal/dataplane")
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	namespace := fmt.Sprintf("waycloak-port-redirect-e2e-%d", time.Now().UnixNano())
	must(t, direct.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	t.Cleanup(func() {
		_ = direct.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})
	no, yes := false, true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: namespace}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, NodeSelector: map[string]string{"kubernetes.io/arch": "amd64"}, Containers: []corev1.Container{{Name: "runner", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, SecurityContext: &corev1.SecurityContext{Privileged: &yes}}}}}
	must(t, direct.Create(context.Background(), pod))
	waitForPodReady(t, direct, pod)
	copyTestBinary(t, binary, namespace, pod.Name)
	setup := "set -eu; apk add --no-cache iproute2 >/dev/null; ip netns add source; ip netns add target; ip link add source0 type veth peer name target0; ip link set source0 netns source; ip link set target0 netns target; ip netns exec source ip link set lo up; ip netns exec source ip address add 172.31.0.1/24 dev source0; ip netns exec source ip link set source0 up; ip netns exec target ip link set lo up; ip netns exec target ip address add 172.31.0.2/24 dev target0; ip netns exec target ip link set target0 up; nohup ip netns exec target env WAYCLOAK_E2E_PORT_REDIRECT=1 /tmp/dataplane.test -test.run '^TestApplicationPortRedirectRotation$' -test.v >/tmp/application-port-test.log 2>&1 </dev/null &"
	command(t, nil, "kubectl", "exec", "-n", namespace, pod.Name, "--", "sh", "-c", setup)
	for generation := 1; generation <= 2; generation++ {
		marker := fmt.Sprintf("/tmp/application-port-ready-%d", generation)
		waitFor(t, 30*time.Second, func() bool { return commandSucceeds(namespace, pod.Name, "test -f "+marker) })
		payload := fmt.Sprintf("generation-%d", generation)
		command(t, nil, "kubectl", "exec", "-n", namespace, pod.Name, "--", "sh", "-ec", fmt.Sprintf("printf %s | ip netns exec source nc -w 3 172.31.0.2 6881", payload))
	}
	waitFor(t, 30*time.Second, func() bool {
		return commandSucceeds(namespace, pod.Name, "grep -q -- '--- PASS: TestApplicationPortRedirectRotation' /tmp/application-port-test.log")
	})
}

func TestVXLANPathAndGatewayLossFailClosed(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	binary := filepath.Join(t.TempDir(), "dataplane.test")
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"), "go", "test", "-c", "-tags=e2e", "-o", binary, "../../internal/dataplane")
	gatewayBinary := filepath.Join(t.TempDir(), "gateway.test")
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"), "go", "test", "-c", "-tags=e2e", "-o", gatewayBinary, "../../internal/gateway")

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	ctx := context.Background()
	namespace := fmt.Sprintf("waycloak-vxlan-e2e-%d", time.Now().UnixNano())
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	must(t, direct.Create(ctx, ns))
	t.Cleanup(func() { _ = direct.Delete(ctx, ns) })

	gateway := netnsRunner("gateway", namespace)
	protected := netnsRunner("protected", namespace)
	external := externalHTTPRunner(namespace)
	must(t, direct.Create(ctx, gateway))
	must(t, direct.Create(ctx, protected))
	must(t, direct.Create(ctx, external))
	waitForPodReady(t, direct, gateway)
	waitForPodReady(t, direct, protected)
	waitForPodReady(t, direct, external)
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(gateway), gateway))
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(protected), protected))
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(external), external))
	command(t, nil, "kubectl", "exec", "-n", namespace, gateway.Name, "--", "wget", "-qO-", "-T", "3", "http://"+external.Status.PodIP+":8080")
	var clusterDNS corev1.Service
	must(t, direct.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "kube-dns"}, &clusterDNS))
	copyTestBinary(t, binary, namespace, gateway.Name)
	copyTestBinary(t, binary, namespace, protected.Name)
	copyLocalFile(t, gatewayBinary, namespace, gateway.Name, "/tmp/gateway.test")

	command(t, nil, "kubectl", "exec", "-n", namespace, gateway.Name, "--", "env", "WAYCLOAK_E2E_GATEWAY_NETWORK=1", "WAYCLOAK_E2E_REMOTE_IP="+protected.Status.PodIP, "/tmp/gateway.test", "-test.run", "^TestConfigureGatewayVXLAN$", "-test.v")
	dnsCommand := fmt.Sprintf("env WAYCLOAK_E2E_GATEWAY_DNS=1 WAYCLOAK_E2E_REMOTE_IP=%s WAYCLOAK_E2E_CLUSTER_DNS=%s /tmp/gateway.test -test.run '^TestServeGatewayDNS$' -test.v >/tmp/gateway-dns.log 2>&1 &", protected.Status.PodIP, clusterDNS.Spec.ClusterIP)
	command(t, nil, "kubectl", "exec", "-n", namespace, gateway.Name, "--", "sh", "-c", dnsCommand)
	waitFor(t, 20*time.Second, func() bool {
		return exec.Command("kubectl", "exec", "-n", namespace, gateway.Name, "--", "test", "-f", "/tmp/gateway-dns-ready").Run() == nil
	})
	gatewayCommand := fmt.Sprintf("env WAYCLOAK_E2E_GATEWAY=1 WAYCLOAK_E2E_SKIP_GATEWAY_VXLAN=1 WAYCLOAK_E2E_SKIP_GATEWAY_DNS=1 WAYCLOAK_E2E_CLUSTER_DNS=%s /tmp/dataplane.test -test.run '^TestFakeGatewayEndpoint$' -test.v >/tmp/gateway.log 2>&1 &", clusterDNS.Spec.ClusterIP)
	command(t, nil, "kubectl", "exec", "-n", namespace, gateway.Name, "--", "sh", "-c", gatewayCommand)
	deadline := time.Now().Add(20 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if exec.Command("kubectl", "exec", "-n", namespace, gateway.Name, "--", "test", "-f", "/tmp/gateway-ready").Run() == nil {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		log, _ := exec.Command("kubectl", "exec", "-n", namespace, gateway.Name, "--", "cat", "/tmp/gateway.log").CombinedOutput()
		t.Fatalf("fake gateway did not become ready:\n%s", log)
	}

	clientEnv := []string{"WAYCLOAK_E2E_CLIENT=1", "WAYCLOAK_E2E_REMOTE_IP=" + gateway.Status.PodIP}
	runInPod(t, namespace, protected.Name, clientEnv, "^TestConfigureVXLANProtectedPath$")
	command(t, nil, "kubectl", "exec", "-n", namespace, gateway.Name, "--", "env", "WAYCLOAK_E2E_GATEWAY_NETWORK=1", "WAYCLOAK_E2E_REMOTE_IP="+protected.Status.PodIP, "/tmp/gateway.test", "-test.run", "^TestConfigureGatewayForwarding$", "-test.v")
	clientEnv = append(clientEnv, "WAYCLOAK_E2E_GATEWAY_FORWARDING=1", "WAYCLOAK_E2E_FORWARD_TARGET="+external.Status.PodIP+":8080")
	response := command(t, nil, "kubectl", "exec", "-n", namespace, protected.Name, "--", "wget", "-qO-", "-T", "3", "http://"+external.Status.PodIP+":8080")
	if strings.TrimSpace(response) != "ok" {
		t.Fatalf("gateway-forwarded response = %q", response)
	}
	runInPod(t, namespace, protected.Name, clientEnv, "^TestRepairOwnedFirewallAndLinkDrift$")
	runInPod(t, namespace, protected.Name, clientEnv, "^TestClusterTrafficModes$")
	runInPod(t, namespace, protected.Name, append(clientEnv, "WAYCLOAK_E2E_EXPECT_GATEWAY=1"), "^TestProtectedStateSurvivesAgentExit$")
	must(t, direct.Delete(ctx, gateway, client.GracePeriodSeconds(0)))
	waitFor(t, 30*time.Second, func() bool {
		var current corev1.Pod
		return direct.Get(ctx, client.ObjectKeyFromObject(gateway), &current) != nil
	})
	runInPod(t, namespace, protected.Name, append(clientEnv, "WAYCLOAK_E2E_EXPECT_GATEWAY=0"), "^TestProtectedStateSurvivesAgentExit$")
}

func externalHTTPRunner(namespace string) *corev1.Pod {
	no := false
	yes := true
	root := int64(0)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: namespace}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: &no,
		NodeSelector:                 map[string]string{"kubernetes.io/arch": "amd64"},
		Containers: []corev1.Container{{Name: "server", Image: "alpine:3.22.1", Command: []string{"sh", "-c", "while true; do printf 'HTTP/1.1 200 OK\\r\\nContent-Length: 2\\r\\n\\r\\nok' | nc -l -p 8080; done"}, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8080)}}, PeriodSeconds: 1}, SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, RunAsNonRoot: &no, RunAsUser: &root, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}}},
	}}
}

func netnsRunner(name, namespace string) *corev1.Pod {
	falseValue := false
	trueValue := true
	runAsRoot := int64(0)
	capabilities := []corev1.Capability{"NET_ADMIN"}
	if name == "gateway" {
		capabilities = append(capabilities, "NET_BIND_SERVICE")
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: &falseValue,
		NodeSelector:                 map[string]string{"kubernetes.io/arch": "amd64"},
		Volumes:                      []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		Containers: []corev1.Container{{Name: "runner", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}, SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &falseValue, RunAsNonRoot: &falseValue, RunAsUser: &runAsRoot,
			Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: capabilities},
			ReadOnlyRootFilesystem: &trueValue,
		}}},
	}}
}

func waitForPodReady(t *testing.T, c client.Client, pod *corev1.Pod) {
	t.Helper()
	waitFor(t, 60*time.Second, func() bool {
		var current corev1.Pod
		if c.Get(context.Background(), client.ObjectKeyFromObject(pod), &current) != nil {
			return false
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
}

func copyTestBinary(t *testing.T, binary, namespace, pod string) {
	t.Helper()
	workingDirectory, err := os.Getwd()
	must(t, err)
	relativeBinary, err := filepath.Rel(workingDirectory, binary)
	must(t, err)
	command(t, nil, "kubectl", "cp", relativeBinary, namespace+"/"+pod+":/tmp/dataplane.test")
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "chmod", "+x", "/tmp/dataplane.test")
}

func runInPod(t *testing.T, namespace, pod string, environment []string, testName string) {
	t.Helper()
	args := []string{"exec", "-n", namespace, pod, "--", "env"}
	args = append(args, environment...)
	args = append(args, "/tmp/dataplane.test", "-test.run", testName, "-test.v")
	command(t, nil, "kubectl", args...)
}
