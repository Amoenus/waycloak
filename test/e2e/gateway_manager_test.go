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

	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGatewayManagerObservesFakeEngineLossAndRecovery(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	binaryDirectory := t.TempDir()
	managerBinary := filepath.Join(binaryDirectory, "gateway-manager")
	fakeBinary := filepath.Join(binaryDirectory, "fake-gluetun")
	buildEnvironment := append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", managerBinary, "../../cmd/gateway-manager")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", fakeBinary, "../fixtures/fake-gluetun")

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	namespace := "waycloak-manager-e2e-" + fmt.Sprint(time.Now().UnixNano())
	ctx := context.Background()
	must(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	t.Cleanup(func() {
		_ = direct.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})
	no := false
	runner := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: namespace}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, NodeSelector: map[string]string{"kubernetes.io/arch": "amd64"}, Containers: []corev1.Container{{Name: "runner", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}}}}}
	must(t, direct.Create(ctx, runner))
	waitForPodReady(t, direct, runner)
	copyLocalFile(t, managerBinary, namespace, runner.Name, "/tmp/gateway-manager")
	copyLocalFile(t, fakeBinary, namespace, runner.Name, "/tmp/fake-gluetun")

	startFakeEngine(t, namespace, runner.Name)
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup /tmp/gateway-manager run --engine-type=Gluetun >/tmp/manager.log 2>&1 & echo $! >/tmp/manager.pid")
	waitFor(t, 20*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/readyz >/dev/null")
	})

	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "kill $(cat /tmp/fake.pid)")
	waitFor(t, 20*time.Second, func() bool {
		return !commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/readyz >/dev/null")
	})
	startFakeEngine(t, namespace, runner.Name)
	waitFor(t, 20*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/readyz >/dev/null")
	})
}

func TestGatewayManagerRenewsProtonNATPMPObservation(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	binaryDirectory := t.TempDir()
	managerBinary := filepath.Join(binaryDirectory, "gateway-manager")
	fakeEngineBinary := filepath.Join(binaryDirectory, "fake-gluetun")
	fakeNATPMPBinary := filepath.Join(binaryDirectory, "fake-natpmp")
	buildEnvironment := append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", managerBinary, "../../cmd/gateway-manager")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", fakeEngineBinary, "../fixtures/fake-gluetun")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", fakeNATPMPBinary, "../fixtures/fake-natpmp")

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	namespace := "waycloak-natpmp-e2e-" + fmt.Sprint(time.Now().UnixNano())
	ctx := context.Background()
	must(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	t.Cleanup(func() { _ = direct.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}) })
	desired := `{"gatewayName":"private","overlayCIDR":"172.30.99.0/24","gatewayAddress":"172.30.99.1","vni":7999,"mtu":1320,"vxlanPort":4789,"tunnelInterface":"tunwaycloak","members":[{"id":"lease-target","overlayAddress":"172.30.99.10","underlayIP":"10.2.0.1"}],"portForwardLeases":[{"identity":"lease-uid","internalPort":49152,"protocols":["TCP","UDP"],"targetAddress":"172.30.99.10","targetPort":6881}]}`
	must(t, direct.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "gateway-config", Namespace: namespace}, Data: map[string]string{"gateway.json": desired}}))
	no := false
	runner := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: namespace}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, NodeSelector: map[string]string{"kubernetes.io/arch": "amd64"}, Containers: []corev1.Container{{Name: "runner", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "gateway-config"}}}}}}}
	must(t, direct.Create(ctx, runner))
	waitForPodReady(t, direct, runner)
	copyLocalFile(t, managerBinary, namespace, runner.Name, "/tmp/gateway-manager")
	copyLocalFile(t, fakeEngineBinary, namespace, runner.Name, "/tmp/fake-gluetun")
	copyLocalFile(t, fakeNATPMPBinary, namespace, runner.Name, "/tmp/fake-natpmp")
	startFakeEngine(t, namespace, runner.Name)
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "ip link add tunwaycloak type dummy && ip address add 10.2.0.2/24 dev tunwaycloak && ip address add 10.2.0.1/32 dev tunwaycloak && ip link set tunwaycloak up")
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup /tmp/fake-natpmp --listen=10.2.0.1:5351 >/tmp/natpmp.log 2>&1 &")
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup /tmp/gateway-manager run --engine-type=Gluetun --config-path=/config/gateway.json --resolv-conf=/etc/resolv.conf --port-forward-driver=ProtonNatPmp --tunnel-interface=tunwaycloak --nat-pmp-gateway-address=10.2.0.1:5351 >/tmp/manager.log 2>&1 &")
	waitFor(t, 20*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/v1/port-forward/leases/lease-uid | grep -q '\"publicPort\":42000'")
	})
	waitFor(t, 20*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/v1/port-forward/leases/lease-uid | grep -q '\"publicPort\":42001'")
	})
}

func TestGatewayManagerAppliesObservedTCPAndUDPDNAT(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	binaryDirectory := t.TempDir()
	managerBinary := filepath.Join(binaryDirectory, "gateway-manager")
	fakeEngineBinary := filepath.Join(binaryDirectory, "fake-gluetun")
	fakeNATPMPBinary := filepath.Join(binaryDirectory, "fake-natpmp")
	buildEnvironment := append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", managerBinary, "../../cmd/gateway-manager")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", fakeEngineBinary, "../fixtures/fake-gluetun")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", fakeNATPMPBinary, "../fixtures/fake-natpmp")

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	namespace := "waycloak-dnat-e2e-" + fmt.Sprint(time.Now().UnixNano())
	ctx := context.Background()
	must(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	t.Cleanup(func() { _ = direct.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}) })
	desired := `{"gatewayName":"private","overlayCIDR":"172.30.99.0/24","gatewayAddress":"172.30.99.1","vni":7999,"mtu":1320,"vxlanPort":4789,"tunnelInterface":"tunwaycloak","members":[{"id":"target","overlayAddress":"172.30.99.10","underlayIP":"192.0.2.2"}],"portForwardLeases":[{"identity":"lease-uid","internalPort":49152,"suggestedExternalPort":42000,"protocols":["TCP","UDP"],"targetAddress":"172.30.99.10","targetPort":6881,"leaseGeneration":1}]}`
	desiredRotated := `{"gatewayName":"private","overlayCIDR":"172.30.99.0/24","gatewayAddress":"172.30.99.1","vni":7999,"mtu":1320,"vxlanPort":4789,"tunnelInterface":"tunwaycloak","members":[{"id":"target","overlayAddress":"172.30.99.10","underlayIP":"192.0.2.2"}],"portForwardLeases":[{"identity":"lease-uid","internalPort":49152,"suggestedExternalPort":42001,"protocols":["TCP","UDP"],"targetAddress":"172.30.99.10","targetPort":6881,"leaseGeneration":2}]}`
	desiredWithSecond := `{"gatewayName":"private","overlayCIDR":"172.30.99.0/24","gatewayAddress":"172.30.99.1","vni":7999,"mtu":1320,"vxlanPort":4789,"tunnelInterface":"tunwaycloak","members":[{"id":"target","overlayAddress":"172.30.99.10","underlayIP":"192.0.2.2"}],"portForwardLeases":[{"identity":"lease-two","internalPort":49153,"protocols":["TCP"],"targetAddress":"172.30.99.10","targetPort":8081,"leaseGeneration":1},{"identity":"lease-uid","internalPort":49152,"suggestedExternalPort":42001,"protocols":["TCP","UDP"],"targetAddress":"172.30.99.10","targetPort":6881,"leaseGeneration":2}]}`
	must(t, direct.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "gateway-config", Namespace: namespace}, Data: map[string]string{"gateway.json": desired}}))
	no, yes := false, true
	runner := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: namespace}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, NodeSelector: map[string]string{"kubernetes.io/arch": "amd64"}, Containers: []corev1.Container{{Name: "runner", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, SecurityContext: &corev1.SecurityContext{Privileged: &yes}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "gateway-config"}}}}}}}
	must(t, direct.Create(ctx, runner))
	waitForPodReady(t, direct, runner)
	copyLocalFile(t, managerBinary, namespace, runner.Name, "/tmp/gateway-manager")
	copyLocalFile(t, fakeEngineBinary, namespace, runner.Name, "/tmp/fake-gluetun")
	copyLocalFile(t, fakeNATPMPBinary, namespace, runner.Name, "/tmp/fake-natpmp")

	setup := "set -eu; apk add --no-cache iproute2 nftables socat; nft add table ip waycloak_e2e_unrelated; ip netns add source; ip netns add target; " +
		"ip link add tunwaycloak type veth peer name src0; ip link set src0 netns source; ip address add 10.2.0.2/24 dev tunwaycloak; ip link set tunwaycloak up; " +
		"ip netns exec source ip link set lo up; ip netns exec source ip address add 10.2.0.1/24 dev src0; ip netns exec source ip address add 10.2.0.3/24 dev src0; ip netns exec source ip link set src0 up; " +
		"ip link add gwunderlay type veth peer name targetunderlay; ip link set targetunderlay netns target; ip address add 192.0.2.1/24 dev gwunderlay; ip link set gwunderlay up; " +
		"ip netns exec target ip link set lo up; ip netns exec target ip address add 192.0.2.2/24 dev targetunderlay; ip netns exec target ip link set targetunderlay up; " +
		"ip netns exec target ip link add targetvx type vxlan id 7999 dev targetunderlay local 192.0.2.2 remote 192.0.2.1 dstport 4789; " +
		"ip netns exec target ip address add 172.30.99.10/24 dev targetvx; ip netns exec target ip link set targetvx up; ip netns exec target ip route add default via 172.30.99.1 dev targetvx"
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", setup)
	startFakeEngine(t, namespace, runner.Name)
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup ip netns exec source /tmp/fake-natpmp --listen=10.2.0.1:5351 >/tmp/natpmp.log 2>&1 &")
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup ip netns exec target sh -c 'while true; do printf tcp-ok | nc -l -p 6881; done' >/tmp/tcp.log 2>&1 &")
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup ip netns exec target socat -u UDP4-RECVFROM:6881,reuseaddr,fork STDOUT >/tmp/udp-received.log 2>/tmp/udp-listener.log &")
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup ip netns exec target sh -c 'while true; do printf tcp-two | nc -l -p 8081; done' >/tmp/tcp-two.log 2>&1 &")
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup /tmp/gateway-manager run --engine-type=Gluetun --config-path=/config/gateway.json --resolv-conf=/etc/resolv.conf --port-forward-driver=ProtonNatPmp --tunnel-interface=tunwaycloak --nat-pmp-gateway-address=10.2.0.1:5351 >/tmp/manager.log 2>&1 &")
	waitFor(t, 30*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/v1/port-forward/leases/lease-uid | grep -q '\"gatewayRulesReady\":true'")
	})
	if overlay := waygateway.OverlayInterfaceName("private"); !commandSucceeds(namespace, runner.Name, "ip link show "+overlay+" >/dev/null") {
		t.Fatalf("gateway overlay %s was not created", overlay)
	}
	if !commandSucceeds(namespace, runner.Name, "ip netns exec source sh -c 'printf request | nc -w 3 10.2.0.2 49152' | grep -q tcp-ok") {
		t.Fatal("TCP mapping did not reach the exact overlay target")
	}
	waitFor(t, 10*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "ip netns exec source sh -c 'printf initial-udp | nc -u -s 10.2.0.1 -p 40001 -w 1 10.2.0.2 49152 >/dev/null || true'; grep -q initial-udp /tmp/udp-received.log")
	})
	updateDesired := func(value string) {
		t.Helper()
		var configMap corev1.ConfigMap
		must(t, direct.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "gateway-config"}, &configMap))
		configMap.Data["gateway.json"] = value
		must(t, direct.Update(ctx, &configMap))
	}
	waitFor(t, 2*time.Minute, func() bool {
		return commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/v1/port-forward/leases/lease-uid | grep -q '\"publicPort\":42001'")
	})
	updateDesired(desiredRotated)
	waitFor(t, 2*time.Minute, func() bool {
		return commandSucceeds(namespace, runner.Name, "payload=$(wget -qO- http://127.0.0.1:18080/v1/port-forward/leases/lease-uid) && echo \"$payload\" | grep -q '\"publicPort\":42001' && echo \"$payload\" | grep -q '\"gatewayRulesGeneration\":2'")
	})
	waitFor(t, 10*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "ip netns exec source sh -c 'printf rotated | nc -w 3 10.2.0.2 49152' | grep -q tcp-ok")
	})
	waitFor(t, 10*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "ip netns exec source sh -c 'printf rotated-udp | nc -u -s 10.2.0.3 -p 40002 -w 1 10.2.0.2 49152 >/dev/null || true'; grep -q rotated-udp /tmp/udp-received.log")
	})
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "nohup ip netns exec source sh -c 'while true; do printf outbound-ok | nc -l -p 9090 -v; done' >/tmp/outbound-source.log 2>&1 &")
	command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "sh", "-c", "ip netns exec target ip route replace default via 172.30.99.1 dev targetvx")
	waitFor(t, 10*time.Second, func() bool {
		return commandSucceeds(namespace, runner.Name, "ip netns exec target sh -c 'nc -p 42001 -w 3 10.2.0.1 9090' | grep -q outbound-ok")
	})
	peerLog := command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "cat", "/tmp/outbound-source.log")
	if !strings.Contains(peerLog, "10.2.0.2]:49152") {
		ruleset := command(t, nil, "kubectl", "exec", "-n", namespace, runner.Name, "--", "nft", "list", "ruleset")
		t.Fatalf("expected provider-facing source 10.2.0.2:49152, got log %q; ruleset:\n%s", peerLog, ruleset)
	}
	updateDesired(desiredWithSecond)
	waitFor(t, 2*time.Minute, func() bool {
		return commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/v1/port-forward/leases/lease-two | grep -q '\"gatewayRulesReady\":true'")
	})
	if !commandSucceeds(namespace, runner.Name, "ip netns exec source sh -c 'printf request | nc -w 3 10.2.0.2 49153' | grep -q tcp-two") {
		t.Fatal("second identity did not reach its exact target")
	}
	if !commandSucceeds(namespace, runner.Name, "ip netns exec source sh -c 'printf request | nc -w 3 10.2.0.2 49152' | grep -q tcp-ok") {
		t.Fatal("adding a second identity disturbed the existing mapping")
	}
	updateDesired(desiredRotated)
	waitFor(t, 2*time.Minute, func() bool {
		return !commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/v1/port-forward/leases/lease-two >/dev/null") && commandSucceeds(namespace, runner.Name, "wget -qO- http://127.0.0.1:18080/v1/port-forward/leases/lease-uid | grep -q '\"gatewayRulesReady\":true'")
	})
	if commandSucceeds(namespace, runner.Name, "ip netns exec source sh -c 'printf stale | nc -w 1 10.2.0.2 49153' | grep -q tcp-two") {
		t.Fatal("removed identity retained stale cross-delivery")
	}
	if !commandSucceeds(namespace, runner.Name, "ip netns exec source sh -c 'printf request | nc -w 3 10.2.0.2 49152' | grep -q tcp-ok") {
		t.Fatal("removing a second identity disturbed the existing mapping")
	}
	if !commandSucceeds(namespace, runner.Name, "nft list table ip waycloak_e2e_unrelated >/dev/null") {
		t.Fatal("gateway reconciliation removed an unrelated nftables table")
	}
}

func startFakeEngine(t *testing.T, namespace, pod string) {
	t.Helper()
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "sh", "-c", "nohup /tmp/fake-gluetun >/tmp/fake.log 2>&1 & echo $! >/tmp/fake.pid")
}

func commandSucceeds(namespace, pod, shellCommand string) bool {
	return execCommand("kubectl", "exec", "-n", namespace, pod, "--", "sh", "-c", shellCommand) == nil
}

func execCommand(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout = nil
	command.Stderr = nil
	return command.Run()
}
