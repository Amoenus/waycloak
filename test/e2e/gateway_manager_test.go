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
