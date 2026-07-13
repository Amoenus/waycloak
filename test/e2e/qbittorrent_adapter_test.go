// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/delivery"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestQBittorrentAdapterAppliesRotatedProviderPort(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	binaryDirectory := t.TempDir()
	adapterBinary := filepath.Join(binaryDirectory, "qbittorrent-adapter")
	fakeAgentBinary := filepath.Join(binaryDirectory, "fake-lease-agent")
	buildEnvironment := append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", adapterBinary, "../../cmd/qbittorrent-adapter")
	command(t, buildEnvironment, "go", "build", "-trimpath", "-o", fakeAgentBinary, "../fixtures/fake-lease-agent")

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	ctx := context.Background()
	namespace := fmt.Sprintf("waycloak-qbit-adapter-e2e-%d", time.Now().UnixNano())
	must(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	t.Cleanup(func() { _ = direct.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}) })

	random := make([]byte, 14)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	apiKey := "qbt_" + hex.EncodeToString(random)
	configuration := "[BitTorrent]\nSession\\Port=6881\nSession\\UseRandomPort=false\n\n[LegalNotice]\nAccepted=true\n\n[Network]\nPortForwardingEnabled=false\n\n[Preferences]\nConnection\\PortRangeMin=6881\nConnection\\UPnP=false\nWebUI\\Address=127.0.0.1\nWebUI\\APIKey=" + apiKey + "\nWebUI\\Port=8080\n"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent-adapter-auth", Namespace: namespace}, StringData: map[string]string{"api-key": apiKey, "qBittorrent.conf": configuration}}
	must(t, direct.Create(ctx, secret))
	now := time.Now().UTC().Truncate(time.Second)
	document := qbitDeliveryDocument(t, "pending-pod-uid", 1, 42000, now)
	fixture := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "lease-fixture", Namespace: namespace}, Data: map[string]string{"port-forward-leases.json": document}}
	must(t, direct.Create(ctx, fixture))

	no := false
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent", Namespace: namespace}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, NodeSelector: map[string]string{"kubernetes.io/arch": "amd64"}, InitContainers: []corev1.Container{{Name: "configure", Image: "alpine:3.22.1", Command: []string{"sh", "-ec"}, Args: []string{"mkdir -p /config/qBittorrent; cp /bootstrap/qBittorrent.conf /config/qBittorrent/qBittorrent.conf; chown -R 1000:1000 /config"}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config"}, {Name: "bootstrap", MountPath: "/bootstrap", ReadOnly: true}}}}, Containers: []corev1.Container{
		{Name: "qbittorrent", Image: "lscr.io/linuxserver/qbittorrent:5.2.3@sha256:352371a7242e8b4aa10958ca02076d1023758070519b89a10251475fb9f1a35a", Env: []corev1.EnvVar{{Name: "PUID", Value: "1000"}, {Name: "PGID", Value: "1000"}, {Name: "TZ", Value: "Etc/UTC"}}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config"}, {Name: "downloads", MountPath: "/downloads"}}},
		{Name: "adapter-fixture", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}, {Name: "lease", MountPath: "/fixtures", ReadOnly: true}, {Name: "adapter-key", MountPath: "/secrets", ReadOnly: true}}},
	}, Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "downloads", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "lease", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: fixture.Name}, Items: []corev1.KeyToPath{{Key: "port-forward-leases.json", Path: "port-forward-leases.json"}}}}}, {Name: "bootstrap", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret.Name, Items: []corev1.KeyToPath{{Key: "qBittorrent.conf", Path: "qBittorrent.conf"}}}}}, {Name: "adapter-key", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret.Name, Items: []corev1.KeyToPath{{Key: "api-key", Path: "api-key"}}}}}}}}
	must(t, direct.Create(ctx, pod))
	waitForPodReady(t, direct, pod)
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(pod), pod))
	initialUID := pod.UID
	copyLocalFile(t, adapterBinary, namespace, pod.Name, "/tmp/qbittorrent-adapter", "adapter-fixture")
	copyLocalFile(t, fakeAgentBinary, namespace, pod.Name, "/tmp/fake-lease-agent", "adapter-fixture")
	command(t, nil, "kubectl", "exec", "-n", namespace, pod.Name, "-c", "adapter-fixture", "--", "sh", "-ec", "chmod +x /tmp/qbittorrent-adapter /tmp/fake-lease-agent; nohup /tmp/fake-lease-agent --document=/fixtures/port-forward-leases.json --state-directory=/tmp >/tmp/fake-agent.log 2>&1 </dev/null &")
	command(t, nil, "kubectl", "exec", "-n", namespace, pod.Name, "-c", "adapter-fixture", "--", "sh", "-ec", "nohup env WAYCLOAK_QBITTORRENT_API_KEY_FILE=/secrets/api-key WAYCLOAK_LEASE_NAME=torrent /tmp/qbittorrent-adapter run >/tmp/adapter.log 2>&1 </dev/null &")
	waitFor(t, 90*time.Second, func() bool {
		return commandSucceedsContainer(namespace, pod.Name, "adapter-fixture", "grep -q '\"generation\":1' /tmp/ack.json && grep -q '\"applicationPort\":42000' /tmp/ack.json") && commandSucceedsContainer(namespace, pod.Name, "qbittorrent", "grep -i ':A410 ' /proc/net/tcp >/dev/null && grep -i ':A410 ' /proc/net/udp >/dev/null")
	})

	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=waycloak-probe&tr=http%3A%2F%2F127.0.0.1%3A18081%2Fannounce"
	command(t, nil, "kubectl", "exec", "-n", namespace, pod.Name, "-c", "qbittorrent", "--", "su", "-s", "/bin/sh", "abc", "-c", "/app/qbittorrent-nox '"+magnet+"'")
	waitFor(t, 60*time.Second, func() bool {
		return commandSucceedsContainer(namespace, pod.Name, "adapter-fixture", "grep -qx 42000 /tmp/tracker-port")
	})

	must(t, direct.Get(ctx, client.ObjectKeyFromObject(fixture), fixture))
	fixture.Data["port-forward-leases.json"] = qbitDeliveryDocument(t, string(initialUID), 2, 42001, now.Add(time.Second))
	must(t, direct.Update(ctx, fixture))
	waitFor(t, 2*time.Minute, func() bool {
		return commandSucceedsContainer(namespace, pod.Name, "adapter-fixture", "grep -q '\"generation\":2' /tmp/ack.json && grep -q '\"applicationPort\":42001' /tmp/ack.json") && commandSucceedsContainer(namespace, pod.Name, "qbittorrent", "grep -i ':A411 ' /proc/net/tcp >/dev/null && grep -i ':A411 ' /proc/net/udp >/dev/null && ! grep -i ':A410 ' /proc/net/tcp >/dev/null")
	})
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(pod), pod))
	if pod.UID != initialUID || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("qBitTorrent rotation replaced the Pod or mounted a Kubernetes API token")
	}
	for _, container := range pod.Spec.Containers {
		if container.SecurityContext != nil && container.SecurityContext.Capabilities != nil && len(container.SecurityContext.Capabilities.Add) != 0 {
			t.Fatalf("container %s received added capabilities", container.Name)
		}
	}
}

func qbitDeliveryDocument(t *testing.T, podUID string, generation int64, port uint16, now time.Time) string {
	t.Helper()
	document := delivery.Document{APIVersion: delivery.APIVersion, PodUID: podUID, Leases: []delivery.Record{{Identity: "lease-uid", Namespace: "apps", Name: "torrent", State: "Active", Gateway: "egress/private", PublicPort: port, TargetPort: 6881, ApplicationPort: port, ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned, Protocols: []string{"TCP", "UDP"}, Generation: generation, IssuedAt: now, RenewAfter: now.Add(10 * time.Minute), ExpiresAt: now.Add(20 * time.Minute)}}}
	serialized, err := delivery.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return serialized
}

func commandSucceedsContainer(namespace, pod, container, script string) bool {
	command := execCommand("kubectl", "exec", "-n", namespace, pod, "-c", container, "--", "sh", "-ec", script)
	return command == nil
}
