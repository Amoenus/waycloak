// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/contract"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestHelmInstallAndAdmissionBoundary(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_HELM") != "1" {
		t.Skip("set WAYCLOAK_E2E_HELM=1 to build, import, and install the source Helm chart")
	}
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	namespace := "waycloak-helm-" + suffix
	release := "waycloak-" + suffix
	controllerTar, controllerTag := buildControllerTarball(t, suffix)

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, policyv1.AddToScheme(scheme))
	must(t, admissionv1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}
	must(t, direct.Create(ctx, ns))
	t.Cleanup(func() { _ = direct.Delete(ctx, ns) })
	nodeName := amd64Node(t, direct)

	loader := imageLoaderPod(namespace, nodeName)
	must(t, direct.Create(ctx, loader))
	waitForPodReady(t, direct, loader)
	copyLocalFile(t, controllerTar, namespace, loader.Name, "/tmp/controller.tar")
	archive := release + "-controller.tar"
	command(t, nil, "kubectl", "exec", "-n", namespace, loader.Name, "--", "cp", "/tmp/controller.tar", "/host/images/"+archive)
	waitFor(t, 60*time.Second, func() bool {
		output, listErr := exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "/host/k3s", "ctr", "--address", "/host/containerd/containerd.sock", "--namespace", "k8s.io", "images", "ls", "-q").CombinedOutput()
		return listErr == nil && strings.Contains(string(output), controllerTag)
	})
	controllerDigest := importedImageDigest(t, namespace, loader.Name, controllerTag)
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "rm", "-f", "/host/images/"+archive).Run()
		_ = exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "/host/k3s", "ctr", "--address", "/host/containerd/containerd.sock", "--namespace", "k8s.io", "images", "rm", controllerTag, "waycloak.test/controller@"+controllerDigest).Run()
	})

	serviceHost := release + "-webhook." + namespace + ".svc"
	cert, key, ca := certificates(t, serviceHost)
	must(t, direct.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "waycloak-webhook-tls", Namespace: namespace}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}))
	dummyDigest := "sha256:" + strings.Repeat("0", 64)
	chartPath := filepath.Join("..", "..", "charts", "waycloak")
	command(t, nil, "helm", "upgrade", "--install", release, chartPath,
		"--namespace", namespace,
		"--set", "images.controller.repository=waycloak.test/controller",
		"--set", "images.controller.digest="+controllerDigest,
		"--set", "images.agent.digest="+dummyDigest,
		"--set", "images.gatewayManager.digest="+dummyDigest,
		"--set", "webhook.tls.existingSecret=waycloak-webhook-tls",
		"--set-string", "webhook.tls.caBundle="+base64.StdEncoding.EncodeToString(ca),
		"--set-string", "nodeSelector.kubernetes\\.io/hostname="+nodeName,
		"--wait", "--timeout", "5m")
	t.Cleanup(func() { _ = exec.Command("helm", "uninstall", release, "--namespace", namespace).Run() })

	keyName := types.NamespacedName{Namespace: namespace, Name: release}
	var deployment appsv1.Deployment
	must(t, direct.Get(ctx, keyName, &deployment))
	if deployment.Status.ReadyReplicas != 2 {
		t.Fatalf("controller ready replicas = %d", deployment.Status.ReadyReplicas)
	}
	var pdb policyv1.PodDisruptionBudget
	must(t, direct.Get(ctx, keyName, &pdb))
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 1 {
		t.Fatalf("controller disruption budget = %#v", pdb.Spec)
	}
	var mutating admissionv1.MutatingWebhookConfiguration
	must(t, direct.Get(ctx, types.NamespacedName{Name: release}, &mutating))
	if len(mutating.Webhooks) != 1 || len(mutating.Webhooks[0].MatchConditions) != 1 || mutating.Webhooks[0].MatchConditions[0].Name != "opted-in" {
		t.Fatalf("mutating webhook boundary = %#v", mutating.Webhooks)
	}

	plain := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: namespace}, Spec: corev1.PodSpec{AutomountServiceAccountToken: boolPtr(false), Containers: []corev1.Container{{Name: "app", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}}}}}
	must(t, direct.Create(ctx, plain))
	t.Cleanup(func() { _ = direct.Delete(ctx, plain, client.GracePeriodSeconds(0)) })
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(plain), plain))
	if len(plain.Spec.InitContainers) != 0 || len(plain.Spec.Containers) != 1 || plain.Annotations[contract.InjectionVersionAnnotation] != "" {
		t.Fatalf("unannotated Pod was changed by the installed chart: %#v", plain.Spec)
	}

	denied := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "missing-gateway", Namespace: namespace, Annotations: map[string]string{contract.GatewayAnnotation: namespace + "/missing"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "alpine:3.22.1"}}}}
	err = direct.Create(ctx, denied)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "gateway") {
		t.Fatalf("annotated Pod did not fail closed through installed admission: %v", err)
	}
}

func buildControllerTarball(t *testing.T, suffix string) (string, string) {
	t.Helper()
	tarball := filepath.Join(t.TempDir(), "controller.tar")
	tag := "e2e-" + suffix
	cmd := exec.Command("go", "run", "github.com/google/ko@v0.19.1", "build", "--push=false", "--tarball="+tarball, "--sbom=spdx", "--platform=linux/amd64", "--bare", "--tags="+tag, "./cmd/controller")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "KO_DOCKER_REPO=waycloak.test/controller", "KO_CONFIG_PATH=.ko.yaml")
	outputBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build controller image tarball: %v\n%s", err, outputBytes)
	}
	return tarball, "waycloak.test/controller:" + tag
}
