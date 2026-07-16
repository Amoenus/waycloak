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

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/Amoenus/waycloak/internal/delivery"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestInjectedPackagedImageLifecycle(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_K3S_IMAGE_IMPORT") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_K3S_IMAGE_IMPORT=1 to authorize node-local image import")
	}
	if strings.HasPrefix(contextName, "kind-") {
		t.Skip("Kind image loading is exercised by the release harness; this node-import path targets authorized k3s")
	}

	controllerBinary := buildController(t)
	dataplaneBinary := filepath.Join(t.TempDir(), "dataplane.test")
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"), "go", "test", "-c", "-tags=e2e", "-o", dataplaneBinary, "../../internal/dataplane")
	suffix := fmt.Sprint(time.Now().UnixNano())
	imageTar, imageTag := buildAgentTarball(t, suffix)

	crdPath := filepath.Join("..", "..", "config", "crd", "bases")
	createdCRDs := missingWaycloakCRDs()
	command(t, nil, "kubectl", "apply", "-f", crdPath)
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, rbacv1.AddToScheme(scheme))
	must(t, admissionv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	ctx := context.Background()
	nodeName := amd64Node(t, direct)
	namespace := "waycloak-image-e2e-" + suffix
	deniedNamespace := namespace + "-denied"
	roleName := namespace
	mutatingName := namespace + "-mutating"
	validatingName := namespace + "-validating"
	t.Cleanup(func() {
		if t.Failed() {
			logPodDiagnostics(t, namespace, "protected")
		}
		_ = direct.Delete(ctx, &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: mutatingName}})
		_ = direct.Delete(ctx, &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: validatingName}})
		_ = direct.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			var controller corev1.Pod
			if apierrors.IsNotFound(direct.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "controller"}, &controller)) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		clearWorkloadFinalizers(t, ctx, direct, namespace)
		_ = direct.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}})
		_ = direct.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}})
		_ = direct.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: deniedNamespace}})
		if len(createdCRDs) != 0 {
			arguments := append([]string{"delete", "crd"}, createdCRDs...)
			arguments = append(arguments, "--ignore-not-found", "--wait=true")
			_ = exec.Command("kubectl", arguments...).Run()
		}
	})

	createInfrastructure(t, direct, namespace, deniedNamespace, roleName)
	var ns corev1.Namespace
	must(t, direct.Get(ctx, types.NamespacedName{Name: namespace}, &ns))
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	ns.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
	ns.Labels["pod-security.kubernetes.io/enforce-version"] = "latest"
	ns.Labels["networking.waycloak.io/e2e-isolated"] = "true"
	must(t, direct.Update(ctx, &ns))

	serviceHost := "waycloak-e2e-webhook." + namespace + ".svc"
	cert, key, ca := certificates(t, serviceHost)
	must(t, direct.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "webhook-certs", Namespace: namespace}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}))
	createRunner(t, direct, namespace)
	waitForPodReady(t, direct, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: namespace}})
	copyLocalFile(t, controllerBinary, namespace, "controller", "/tmp/waycloak-controller")

	loader := imageLoaderPod(namespace, nodeName)
	must(t, direct.Create(ctx, loader))
	waitForPodReady(t, direct, loader)
	copyLocalFile(t, imageTar, namespace, loader.Name, "/tmp/agent.tar")
	imageArchive := "waycloak-" + suffix + ".tar"
	command(t, nil, "kubectl", "exec", "-n", namespace, loader.Name, "--", "cp", "/tmp/agent.tar", "/host/images/"+imageArchive)
	waitFor(t, 60*time.Second, func() bool {
		output, listErr := exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "/host/k3s", "ctr", "--address", "/host/containerd/containerd.sock", "--namespace", "k8s.io", "images", "ls", "-q").CombinedOutput()
		return listErr == nil && strings.Contains(string(output), imageTag)
	})
	imageDigest := importedImageDigest(t, namespace, loader.Name, imageTag)
	imageRef := imageTag + "@" + imageDigest
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "rm", "-f", "/host/images/"+imageArchive).Run()
		_ = exec.Command("kubectl", "exec", "-n", namespace, loader.Name, "--", "/host/k3s", "ctr", "--address", "/host/containerd/containerd.sock", "--namespace", "k8s.io", "images", "rm", imageTag, "waycloak.test/agent@"+imageDigest).Run()
	})

	startControllerWithImage(t, namespace, true, imageRef)
	mutating, validating := webhookConfigurations(mutatingName, validatingName, namespace, ca)
	must(t, direct.Create(ctx, mutating))
	must(t, direct.Create(ctx, validating))

	gatewayPod := netnsRunner("gateway", namespace)
	gatewayPod.Spec.NodeSelector = nil
	gatewayPod.Spec.NodeName = nodeName
	must(t, direct.Create(ctx, gatewayPod))
	waitForPodReady(t, direct, gatewayPod)
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(gatewayPod), gatewayPod))
	copyTestBinary(t, dataplaneBinary, namespace, gatewayPod.Name)
	var clusterDNS corev1.Service
	must(t, direct.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "kube-dns"}, &clusterDNS))
	var kubernetesService corev1.Service
	must(t, direct.Get(ctx, client.ObjectKey{Namespace: "default", Name: "kubernetes"}, &kubernetesService))

	gw := gateway(namespace)
	gw.Spec.Overlay.CIDR = "172.30.99.0/24"
	gw.Spec.ClusterTraffic.Mode = "Gateway"
	must(t, direct.Create(ctx, gw))
	waitFor(t, 20*time.Second, func() bool { return direct.Get(ctx, client.ObjectKeyFromObject(gw), gw) == nil })
	updateGatewayEndpoint(t, direct, gw, gatewayPod.Status.PodIP+":4789", 18080)

	protected := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: namespace, Annotations: map[string]string{contract.GatewayAnnotation: namespace + "/private", contract.PortForwardContainerAnnotation: "app"}}, Spec: corev1.PodSpec{NodeName: nodeName, AutomountServiceAccountToken: boolPtr(false), Containers: []corev1.Container{{Name: "app", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}}}}}
	must(t, direct.Create(ctx, protected))
	waitFor(t, 30*time.Second, func() bool {
		return direct.Get(ctx, client.ObjectKeyFromObject(protected), protected) == nil && protected.Status.PodIP != ""
	})
	allocationKey := client.ObjectKey{Namespace: namespace, Name: protected.Annotations[contract.AllocationNameAnnotation]}
	gatewayCommand := fmt.Sprintf("env WAYCLOAK_E2E_GATEWAY=1 WAYCLOAK_E2E_LOCAL_IP=%s WAYCLOAK_E2E_REMOTE_IP=%s WAYCLOAK_E2E_CLUSTER_DNS=%s /tmp/dataplane.test -test.run '^TestFakeGatewayEndpoint$' -test.v >/tmp/gateway.log 2>&1 &", gatewayPod.Status.PodIP, protected.Status.PodIP, clusterDNS.Spec.ClusterIP)
	command(t, nil, "kubectl", "exec", "-n", namespace, gatewayPod.Name, "--", "sh", "-c", gatewayCommand)
	waitFor(t, 20*time.Second, func() bool {
		return exec.Command("kubectl", "exec", "-n", namespace, gatewayPod.Name, "--", "test", "-f", "/tmp/gateway-ready").Run() == nil
	})
	waitForPodReady(t, direct, protected)
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(protected), protected))
	protectedUID := protected.UID
	assertInjectedRuntime(t, protected, imageRef)
	assertFilteredDeliveryMount(t, protected)
	command(t, nil, "kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "sh", "-c", "test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token && set -- /run/waycloak/port-forward/* && test $# -eq 1 && test $(basename \"$1\") = port-forward-leases.json")

	must(t, direct.Delete(ctx, gatewayPod, client.GracePeriodSeconds(0)))
	waitFor(t, 30*time.Second, func() bool { return !podReady(t, direct, protected) })
	replacementGateway := netnsRunner("gateway-replacement", namespace)
	replacementGateway.Spec.NodeSelector = nil
	replacementGateway.Spec.NodeName = nodeName
	must(t, direct.Create(ctx, replacementGateway))
	waitForPodReady(t, direct, replacementGateway)
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(replacementGateway), replacementGateway))
	copyTestBinary(t, dataplaneBinary, namespace, replacementGateway.Name)
	replacementCommand := fmt.Sprintf("env WAYCLOAK_E2E_GATEWAY=1 WAYCLOAK_E2E_LOCAL_IP=%s WAYCLOAK_E2E_REMOTE_IP=%s WAYCLOAK_E2E_CLUSTER_DNS=%s /tmp/dataplane.test -test.run '^TestFakeGatewayEndpoint$' -test.v >/tmp/gateway.log 2>&1 &", replacementGateway.Status.PodIP, protected.Status.PodIP, clusterDNS.Spec.ClusterIP)
	command(t, nil, "kubectl", "exec", "-n", namespace, replacementGateway.Name, "--", "sh", "-c", replacementCommand)
	waitFor(t, 20*time.Second, func() bool {
		return exec.Command("kubectl", "exec", "-n", namespace, replacementGateway.Name, "--", "test", "-f", "/tmp/gateway-ready").Run() == nil
	})
	updateGatewayEndpoint(t, direct, gw, replacementGateway.Status.PodIP+":4789", 18080)
	waitFor(t, 90*time.Second, func() bool {
		var current corev1.ConfigMap
		return direct.Get(ctx, allocationKey, &current) == nil && current.Data["gatewayEndpoint"] == replacementGateway.Status.PodIP+":4789"
	})
	waitForPodReady(t, direct, protected)
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(protected), protected))
	if protected.UID != protectedUID {
		t.Fatalf("gateway replacement changed protected Pod UID: %s -> %s", protectedUID, protected.UID)
	}
	command(t, nil, "kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "getent", "hosts", "kubernetes.default")
	gatewayPod = replacementGateway

	stopController(t, namespace)
	identity := "e2e-renewable-lease"
	publishDeliveryDocument(t, direct, protected, identity, 1, 42000)
	waitFor(t, 30*time.Second, func() bool {
		output, readErr := exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "wget", "-qO-", "http://127.0.0.1:9809/v1/port-forward/leases/"+identity).CombinedOutput()
		return readErr == nil && strings.Contains(string(output), `"generation":1`) && strings.Contains(string(output), `"publicPort":42000`)
	})
	publishDeliveryDocument(t, direct, protected, identity, 2, 42001)
	waitFor(t, 30*time.Second, func() bool {
		fileOutput, fileErr := exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "cat", contract.ApplicationLeaseMountPath+"/"+contract.PortForwardLeasesKey).CombinedOutput()
		apiOutput, apiErr := exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "wget", "-qO-", "http://127.0.0.1:9809/v1/port-forward/leases/"+identity).CombinedOutput()
		return fileErr == nil && apiErr == nil && strings.Contains(string(fileOutput), `"generation":2`) && strings.Contains(string(apiOutput), `"generation":2`) && strings.Contains(string(apiOutput), `"publicPort":42001`)
	})
	command(t, nil, "kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "getent", "hosts", "kubernetes.default")

	must(t, direct.Delete(ctx, gatewayPod, client.GracePeriodSeconds(0)))
	waitFor(t, 30*time.Second, func() bool { return !podReady(t, direct, protected) })
	if exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "getent", "hosts", "kubernetes.default").Run() == nil {
		t.Fatal("application DNS bypassed the deleted gateway")
	}
	if exec.Command("kubectl", "exec", "-n", namespace, protected.Name, "-c", "app", "--", "nc", "-z", "-w", "2", kubernetesService.Spec.ClusterIP, "443").Run() == nil {
		t.Fatal("application reached the ordinary cluster path after gateway loss")
	}

	must(t, direct.Delete(ctx, protected, client.GracePeriodSeconds(0)))
	waitFor(t, 30*time.Second, func() bool {
		var current corev1.Pod
		return apierrors.IsNotFound(direct.Get(ctx, client.ObjectKeyFromObject(protected), &current))
	})
	plain := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "plain-restored", Namespace: namespace}, Spec: corev1.PodSpec{NodeName: nodeName, AutomountServiceAccountToken: boolPtr(false), Containers: []corev1.Container{{Name: "app", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}}}}}
	must(t, direct.Create(ctx, plain))
	waitForPodReady(t, direct, plain)
	command(t, nil, "kubectl", "exec", "-n", namespace, plain.Name, "--", "getent", "hosts", "kubernetes.default")
}

func missingWaycloakCRDs() []string {
	names := []string{
		"portforwardleases.networking.waycloak.io",
		"vpngateways.networking.waycloak.io",
		"vpnworkloads.networking.waycloak.io",
		"workloadadapters.networking.waycloak.io",
	}
	missing := make([]string, 0, len(names))
	for _, name := range names {
		if exec.Command("kubectl", "get", "crd", name).Run() != nil {
			missing = append(missing, name)
		}
	}
	return missing
}

func buildAgentTarball(t *testing.T, suffix string) (string, string) {
	t.Helper()
	tarball := filepath.Join(t.TempDir(), "agent.tar")
	tag := "e2e-" + suffix
	cmd := exec.Command("go", "run", "github.com/google/ko@v0.19.1", "build", "--push=false", "--tarball="+tarball, "--sbom=spdx", "--platform=linux/amd64", "--bare", "--tags="+tag, "./cmd/agent")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "KO_DOCKER_REPO=waycloak.test/agent", "KO_CONFIG_PATH=.ko.yaml")
	outputBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build agent image tarball: %v\n%s", err, outputBytes)
	}
	output := string(outputBytes)
	var imageRef string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "waycloak.test/agent:") {
			imageRef = strings.TrimSpace(line)
		}
	}
	if imageRef == "" {
		t.Fatalf("ko did not return an image reference:\n%s", output)
	}
	return tarball, "waycloak.test/agent:" + tag
}

func importedImageDigest(t *testing.T, namespace, loader, imageTag string) string {
	t.Helper()
	output := command(t, nil, "kubectl", "exec", "-n", namespace, loader, "--", "/host/k3s", "ctr", "--address", "/host/containerd/containerd.sock", "--namespace", "k8s.io", "images", "ls", "-q")
	digestPrefix := strings.SplitN(imageTag, ":", 2)[0] + "@sha256:"
	for _, line := range strings.Split(output, "\n") {
		ref := strings.TrimSpace(line)
		if strings.HasPrefix(ref, digestPrefix) {
			return strings.TrimPrefix(ref, strings.SplitN(imageTag, ":", 2)[0]+"@")
		}
	}
	t.Fatalf("containerd did not report a digest for %s:\n%s", imageTag, output)
	return ""
}

func clearWorkloadFinalizers(t *testing.T, ctx context.Context, c client.Client, namespace string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var workloads wayv1.VPNWorkloadList
		if err := c.List(ctx, &workloads, client.InNamespace(namespace)); apierrors.IsNotFound(err) {
			return
		} else if err != nil {
			t.Logf("list VPNWorkloads during cleanup: %v", err)
			return
		}
		if len(workloads.Items) == 0 {
			return
		}
		for i := range workloads.Items {
			workload := &workloads.Items[i]
			if len(workload.Finalizers) != 0 {
				if err := c.Patch(ctx, workload, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/metadata/finalizers"}]`))); err != nil && !apierrors.IsNotFound(err) {
					t.Logf("clear VPNWorkload finalizer during cleanup: %v", err)
				}
			}
			_ = c.Delete(ctx, workload)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("timed out clearing VPNWorkloads in %s", namespace)
}

func logPodDiagnostics(t *testing.T, namespace, pod string) {
	t.Helper()
	commands := [][]string{
		{"get", "pod", "-n", namespace, pod, "-o", "wide"},
		{"describe", "pod", "-n", namespace, pod},
		{"logs", "-n", namespace, pod, "-c", contract.AgentContainer, "--tail=100"},
		{"logs", "-n", namespace, pod, "-c", contract.VerifyContainer, "--tail=100"},
	}
	for _, args := range commands {
		output, err := exec.Command("kubectl", args...).CombinedOutput()
		t.Logf("kubectl %s (error=%v):\n%s", strings.Join(args, " "), err, output)
	}
}

func amd64Node(t *testing.T, c client.Client) string {
	t.Helper()
	var nodes corev1.NodeList
	must(t, c.List(context.Background(), &nodes, client.MatchingLabels{"kubernetes.io/arch": "amd64"}))
	for _, node := range nodes.Items {
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				return node.Name
			}
		}
	}
	t.Fatal("no Ready amd64 node")
	return ""
}

func imageLoaderPod(namespace, node string) *corev1.Pod {
	falseValue := false
	trueValue := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "image-loader", Namespace: namespace}, Spec: corev1.PodSpec{NodeName: node, AutomountServiceAccountToken: &falseValue, Containers: []corev1.Container{{Name: "loader", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, SecurityContext: &corev1.SecurityContext{Privileged: &trueValue}, VolumeMounts: []corev1.VolumeMount{{Name: "k3s", MountPath: "/host/k3s", ReadOnly: true}, {Name: "containerd", MountPath: "/host/containerd", ReadOnly: true}, {Name: "images", MountPath: "/host/images"}, {Name: "tmp", MountPath: "/tmp"}}}}, Volumes: []corev1.Volume{{Name: "k3s", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/opt/bin/k3s", Type: hostPathType(corev1.HostPathFile)}}}, {Name: "containerd", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/run/k3s/containerd", Type: hostPathType(corev1.HostPathDirectory)}}}, {Name: "images", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/rancher/k3s/agent/images", Type: hostPathType(corev1.HostPathDirectoryOrCreate)}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}}
}

func hostPathType(value corev1.HostPathType) *corev1.HostPathType { return &value }

func copyLocalFile(t *testing.T, local, namespace, pod, remote string, container ...string) {
	t.Helper()
	workingDirectory, err := os.Getwd()
	must(t, err)
	relative, err := filepath.Rel(workingDirectory, local)
	must(t, err)
	copyArguments := []string{"cp", relative, namespace + "/" + pod + ":" + remote}
	execArguments := []string{"exec", "-n", namespace, pod}
	if len(container) > 1 {
		t.Fatalf("copyLocalFile accepts at most one container, got %d", len(container))
	}
	if len(container) == 1 {
		copyArguments = append(copyArguments, "-c", container[0])
		execArguments = append(execArguments, "-c", container[0])
	}
	command(t, nil, "kubectl", copyArguments...)
	execArguments = append(execArguments, "--", "chmod", "+x", remote)
	command(t, nil, "kubectl", execArguments...)
}

func updateGatewayEndpoint(t *testing.T, c client.Client, gateway *wayv1.VPNGateway, endpoint string, port int32) {
	t.Helper()
	waitFor(t, 20*time.Second, func() bool {
		var current wayv1.VPNGateway
		if c.Get(context.Background(), client.ObjectKeyFromObject(gateway), &current) != nil {
			return false
		}
		current.Status.Overlay = wayv1.GatewayOverlayStatus{Endpoint: endpoint, HealthPort: port}
		return c.Status().Update(context.Background(), &current) == nil
	})
}

func assertInjectedRuntime(t *testing.T, pod *corev1.Pod, image string) {
	t.Helper()
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("protected Pod received a service account token")
	}
	if pod.Spec.Containers[0].Name != "app" || pod.Spec.Containers[0].SecurityContext != nil {
		t.Fatalf("application security context was modified: %#v", pod.Spec.Containers[0].SecurityContext)
	}
	allContainers := append([]corev1.Container{}, pod.Spec.InitContainers...)
	allContainers = append(allContainers, pod.Spec.Containers...)
	for _, container := range allContainers {
		if strings.HasPrefix(container.Name, "waycloak-") && container.Image != image {
			t.Fatalf("injected container %s image=%s", container.Name, container.Image)
		}
	}
}

func assertFilteredDeliveryMount(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == contract.PortForwardVolume {
			if volume.ConfigMap == nil || len(volume.ConfigMap.Items) != 1 || volume.ConfigMap.Items[0].Key != contract.PortForwardLeasesKey || volume.ConfigMap.Items[0].Path != contract.PortForwardLeasesKey {
				t.Fatalf("port-forward delivery volume is not filtered: %#v", volume.ConfigMap)
			}
			return
		}
	}
	t.Fatal("filtered port-forward delivery volume is missing")
}

func publishDeliveryDocument(t *testing.T, c client.Client, pod *corev1.Pod, identity string, generation int64, publicPort uint16) {
	t.Helper()
	now := time.Now().UTC()
	document, err := delivery.Marshal(delivery.Document{APIVersion: delivery.APIVersion, PodUID: string(pod.UID), Leases: []delivery.Record{{Identity: identity, Namespace: pod.Namespace, Name: "e2e", State: "Active", Gateway: pod.Namespace + "/private", PublicPort: publicPort, TargetPort: 6881, Protocols: []string{"TCP", "UDP"}, Generation: generation, IssuedAt: now.Add(-time.Second), RenewAfter: now.Add(time.Minute), ExpiresAt: now.Add(2 * time.Minute)}}})
	must(t, err)
	ctx := context.Background()
	var cm corev1.ConfigMap
	must(t, c.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Annotations[contract.AllocationNameAnnotation]}, &cm))
	cm.Data[contract.PortForwardLeasesKey] = document
	must(t, c.Update(ctx, &cm))
	var current corev1.Pod
	must(t, c.Get(ctx, client.ObjectKeyFromObject(pod), &current))
	if current.Annotations == nil {
		current.Annotations = map[string]string{}
	}
	current.Annotations[contract.DeliveryDigestAnnotation] = contract.DeliveryDigest(document)
	must(t, c.Update(ctx, &current))
}

func podReady(t *testing.T, c client.Client, pod *corev1.Pod) bool {
	t.Helper()
	var current corev1.Pod
	if c.Get(context.Background(), client.ObjectKeyFromObject(pod), &current) != nil {
		return false
	}
	for _, condition := range current.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
