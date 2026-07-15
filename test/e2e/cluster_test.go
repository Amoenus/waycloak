//go:build e2e

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const e2eAgentImage = "registry.k8s.io/pause@sha256:1111111111111111111111111111111111111111111111111111111111111111"
const e2eAdmissionGeneration = "1111111111111111111111111111111111111111111111111111111111111111"
const e2eAdmissionGenerationConfigMap = "admission-generation"

func TestAdmissionAndAllocationLifecycle(t *testing.T) {
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}
	binary := buildController(t)
	crdPath := filepath.Join("..", "..", "config", "crd", "bases")
	command(t, nil, "kubectl", "apply", "-f", crdPath)

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, rbacv1.AddToScheme(scheme))
	must(t, admissionv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)

	suffix := fmt.Sprint(time.Now().UnixNano())
	namespace := "waycloak-e2e-" + suffix
	deniedNamespace := namespace + "-denied"
	roleName := namespace
	mutatingName := namespace + "-mutating"
	validatingName := namespace + "-validating"
	ctx := context.Background()
	t.Cleanup(func() {
		_ = direct.Delete(ctx, &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: mutatingName}})
		_ = direct.Delete(ctx, &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: validatingName}})
		var workloads wayv1.VPNWorkloadList
		if err := direct.List(ctx, &workloads, client.InNamespace(namespace)); err == nil {
			for i := range workloads.Items {
				item := &workloads.Items[i]
				item.Finalizers = nil
				_ = direct.Update(ctx, item)
				_ = direct.Delete(ctx, item)
			}
		}
		var gateways wayv1.VPNGatewayList
		if err := direct.List(ctx, &gateways, client.InNamespace(namespace)); err == nil {
			for i := range gateways.Items {
				_ = direct.Delete(ctx, &gateways.Items[i])
			}
		}
		var leases wayv1.PortForwardLeaseList
		if err := direct.List(ctx, &leases, client.InNamespace(namespace)); err == nil {
			for i := range leases.Items {
				item := &leases.Items[i]
				item.Finalizers = nil
				_ = direct.Update(ctx, item)
				_ = direct.Delete(ctx, item)
			}
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var remainingWorkloads wayv1.VPNWorkloadList
			var remainingGateways wayv1.VPNGatewayList
			var remainingLeases wayv1.PortForwardLeaseList
			workloadsGone := direct.List(ctx, &remainingWorkloads, client.InNamespace(namespace)) != nil || len(remainingWorkloads.Items) == 0
			gatewaysGone := direct.List(ctx, &remainingGateways, client.InNamespace(namespace)) != nil || len(remainingGateways.Items) == 0
			leasesGone := direct.List(ctx, &remainingLeases, client.InNamespace(namespace)) != nil || len(remainingLeases.Items) == 0
			if workloadsGone && gatewaysGone && leasesGone {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		_ = direct.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}})
		_ = direct.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}})
		_ = direct.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
		_ = direct.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: deniedNamespace}})
		_ = exec.Command("kubectl", "delete", "-f", crdPath, "--ignore-not-found", "--wait=true", "--timeout=30s").Run()
	})

	createInfrastructure(t, direct, namespace, deniedNamespace, roleName)
	serviceHost := "waycloak-e2e-webhook." + namespace + ".svc"
	cert, key, ca := certificates(t, serviceHost)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "webhook-certs", Namespace: namespace}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}
	must(t, direct.Create(ctx, secret))
	must(t, direct.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: e2eAdmissionGenerationConfigMap, Namespace: namespace}, Data: map[string]string{contract.AdmissionGenerationKey: e2eAdmissionGeneration}}))
	createRunner(t, direct, namespace)
	waitFor(t, 60*time.Second, func() bool {
		var runner corev1.Pod
		if direct.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "controller"}, &runner) != nil {
			return false
		}
		for _, condition := range runner.Status.Conditions {
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
	command(t, nil, "kubectl", "cp", relativeBinary, namespace+"/controller:/tmp/waycloak-controller")
	command(t, nil, "kubectl", "exec", "-n", namespace, "controller", "--", "chmod", "+x", "/tmp/waycloak-controller")
	startController(t, namespace, false)

	mutating, validating := webhookConfigurations(mutatingName, validatingName, namespace, ca)
	must(t, direct.Create(ctx, mutating))
	must(t, direct.Create(ctx, validating))

	gw := gateway(namespace)
	must(t, direct.Create(ctx, gw))
	waitFor(t, 20*time.Second, func() bool {
		probe := pod("webhook-probe", deniedNamespace, map[string]string{contract.GatewayAnnotation: namespace + "/private"})
		err := direct.Create(ctx, probe, client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "UnauthorizedGateway")
	})

	plain := pod("plain", namespace, nil)
	must(t, direct.Create(ctx, plain, client.DryRunAll))
	assertUnmutated(t, plain)
	denied := pod("denied", deniedNamespace, map[string]string{contract.GatewayAnnotation: namespace + "/private"})
	err = direct.Create(ctx, denied, client.DryRunAll)
	if err == nil || !strings.Contains(err.Error(), "UnauthorizedGateway") {
		t.Fatalf("unauthorized gateway result: %v", err)
	}
	generated := pod("", namespace, map[string]string{contract.GatewayAnnotation: namespace + "/private"})
	generated.GenerateName = "generated-protected-"
	must(t, direct.Create(ctx, generated))
	if generated.Name == "" || !contract.IsAllocationConfigMapName(generated.Annotations[contract.AllocationNameAnnotation]) {
		t.Fatalf("generated Pod admission marker is invalid: name=%q annotations=%#v", generated.Name, generated.Annotations)
	}
	must(t, direct.Delete(ctx, generated, client.GracePeriodSeconds(0)))

	protected := pod("protected", namespace, map[string]string{contract.GatewayAnnotation: namespace + "/private"})
	protected.Spec.AutomountServiceAccountToken = nil
	protected.Spec.NodeName = "waycloak-e2e-nonexistent-node"
	protected.Labels = map[string]string{"app": "protected"}
	must(t, direct.Create(ctx, protected))
	if protected.Annotations[contract.InjectionVersionAnnotation] != contract.InjectionVersion {
		t.Fatalf("Pod was not injected: %#v", protected.Annotations)
	}
	if protected.Spec.AutomountServiceAccountToken == nil || *protected.Spec.AutomountServiceAccountToken {
		t.Fatal("protected Pod did not disable service-account token automount")
	}
	for _, volume := range protected.Spec.Volumes {
		if volume.Projected == nil {
			continue
		}
		for _, source := range volume.Projected.Sources {
			if source.ServiceAccountToken != nil {
				t.Fatalf("protected Pod retained service-account token volume %q", volume.Name)
			}
		}
	}
	cmKey := types.NamespacedName{Namespace: namespace, Name: protected.Annotations[contract.AllocationNameAnnotation]}
	var cm corev1.ConfigMap
	if err = direct.Get(ctx, cmKey, &cm); !apierrors.IsNotFound(err) {
		t.Fatalf("allocation ConfigMap existed before controller: %v", err)
	}
	time.Sleep(3 * time.Second)
	var observed corev1.Pod
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(protected), &observed))
	for _, state := range observed.Status.ContainerStatuses {
		if state.Name == "app" && state.State.Running != nil {
			t.Fatal("application started before allocation ConfigMap existed")
		}
	}

	startController(t, namespace, true)
	waitFor(t, 30*time.Second, func() bool { return direct.Get(ctx, cmKey, &cm) == nil })
	nativeConfig := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "gluetun-native", Namespace: namespace}, Data: map[string]string{"VPN_SERVICE_PROVIDER": "mullvad", "VPN_TYPE": "wireguard"}}
	must(t, direct.Create(ctx, nativeConfig))
	nativeGateway := gateway(namespace)
	nativeGateway.Name = "native"
	nativeGateway.Spec.Provider = nil
	nativeGateway.Spec.PortForwarding = wayv1.PortForwardingSpec{}
	nativeGateway.Spec.Engine.Config = &wayv1.EngineNativeConfigSpec{EnvFrom: []corev1.LocalObjectReference{{Name: nativeConfig.Name}}, Files: []wayv1.EngineFileSource{{SecretRef: &corev1.LocalObjectReference{Name: "wireguard-config"}, MountPath: "/gluetun/wireguard"}}}
	must(t, direct.Create(ctx, nativeGateway))
	waitFor(t, 30*time.Second, func() bool {
		if direct.Get(ctx, client.ObjectKeyFromObject(nativeGateway), nativeGateway) != nil {
			return false
		}
		accepted := apiMeta.FindStatusCondition(nativeGateway.Status.Conditions, waystatus.ConditionAccepted)
		return accepted != nil && accepted.Status == metav1.ConditionTrue
	})
	nativeConfig.Data["VPN_INTERFACE"] = "conflicting-interface"
	must(t, direct.Update(ctx, nativeConfig))
	waitFor(t, 30*time.Second, func() bool {
		if direct.Get(ctx, client.ObjectKeyFromObject(nativeGateway), nativeGateway) != nil {
			return false
		}
		accepted := apiMeta.FindStatusCondition(nativeGateway.Status.Conditions, waystatus.ConditionAccepted)
		return accepted != nil && accepted.Status == metav1.ConditionFalse && accepted.Reason == waystatus.ReasonInvalidEngineConfiguration && !strings.Contains(accepted.Message, "conflicting-interface")
	})
	delete(nativeConfig.Data, "VPN_INTERFACE")
	must(t, direct.Update(ctx, nativeConfig))
	waitFor(t, 30*time.Second, func() bool {
		if direct.Get(ctx, client.ObjectKeyFromObject(nativeGateway), nativeGateway) != nil {
			return false
		}
		accepted := apiMeta.FindStatusCondition(nativeGateway.Status.Conditions, waystatus.ConditionAccepted)
		return accepted != nil && accepted.Status == metav1.ConditionTrue
	})
	var workloads wayv1.VPNWorkloadList
	must(t, direct.List(ctx, &workloads, client.InNamespace(namespace)))
	if len(workloads.Items) != 1 {
		t.Fatalf("workload registrations=%d", len(workloads.Items))
	}
	firstKey := client.ObjectKeyFromObject(&workloads.Items[0])
	firstAddress := workloads.Items[0].Status.Allocation.Address
	if firstAddress != "172.30.99.2" {
		t.Fatalf("first allocation=%q", firstAddress)
	}
	if cm.Data["podUID"] != string(protected.UID) {
		t.Fatalf("ConfigMap UID=%q Pod UID=%q", cm.Data["podUID"], protected.UID)
	}
	var readyTarget corev1.Pod
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(protected), &readyTarget))
	readyTarget.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	must(t, direct.Status().Update(ctx, &readyTarget))
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: namespace}, Spec: wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedNameReference{Name: gw.Name}, Target: wayv1.PortForwardTargetSpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "protected"}}, Port: 6881}, Protocols: []wayv1.PortForwardProtocol{wayv1.PortForwardProtocolTCP, wayv1.PortForwardProtocolUDP}}}
	must(t, direct.Create(ctx, lease))
	var currentLease wayv1.PortForwardLease
	leaseReady := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if direct.Get(ctx, client.ObjectKeyFromObject(lease), &currentLease) != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		target := apiMeta.FindStatusCondition(currentLease.Status.Conditions, waystatus.ConditionTargetReady)
		provider := apiMeta.FindStatusCondition(currentLease.Status.Conditions, waystatus.ConditionProviderLeaseReady)
		leaseReady = target != nil && target.Status == metav1.ConditionTrue && provider != nil && provider.Status == metav1.ConditionFalse && provider.Reason == waystatus.ReasonProviderLeaseObservationFailed && currentLease.Status.Target != nil && currentLease.Status.Target.PodRef.UID == protected.UID && currentLease.Status.ProviderInternalPort == 49152
		if leaseReady {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !leaseReady {
		controllerLog := command(t, nil, "kubectl", "exec", "-n", namespace, "controller", "--", "sh", "-c", "tail -n 100 /tmp/controller.log 2>/dev/null || true")
		t.Fatalf("port-forward lease did not reach expected target/provider state: status=%#v controller log:\n%s", currentLease.Status, controllerLog)
	}
	invalidLease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: namespace}, Spec: wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedNameReference{Name: gw.Name}, Target: wayv1.PortForwardTargetSpec{PodSelector: metav1.LabelSelector{}, Port: 6881}, Protocols: []wayv1.PortForwardProtocol{wayv1.PortForwardProtocolTCP}}}
	if err := direct.Create(ctx, invalidLease); err == nil {
		t.Fatal("API server accepted an empty port-forward target selector")
	}

	startController(t, namespace, true)
	waitFor(t, 20*time.Second, func() bool {
		var item wayv1.VPNWorkload
		return direct.Get(ctx, firstKey, &item) == nil && item.Status.Allocation.Address == firstAddress
	})
	second := pod("second", namespace, map[string]string{contract.GatewayAnnotation: namespace + "/private"})
	must(t, direct.Create(ctx, second))
	secondCM := types.NamespacedName{Namespace: namespace, Name: second.Annotations[contract.AllocationNameAnnotation]}
	waitFor(t, 30*time.Second, func() bool { var item corev1.ConfigMap; return direct.Get(ctx, secondCM, &item) == nil })
	assertAllocation(t, direct, firstKey, firstAddress)
	must(t, direct.Delete(ctx, second, client.GracePeriodSeconds(0)))
	waitFor(t, 20*time.Second, func() bool {
		var item corev1.Pod
		return apierrors.IsNotFound(direct.Get(ctx, client.ObjectKeyFromObject(second), &item))
	})
	assertAllocation(t, direct, firstKey, firstAddress)

	must(t, direct.Delete(ctx, protected, client.GracePeriodSeconds(0)))
	waitFor(t, 20*time.Second, func() bool {
		var item corev1.Pod
		return apierrors.IsNotFound(direct.Get(ctx, client.ObjectKeyFromObject(protected), &item))
	})
	waitFor(t, 20*time.Second, func() bool {
		var list wayv1.VPNWorkloadList
		_ = direct.List(ctx, &list, client.InNamespace(namespace))
		return len(list.Items) == 0
	})
	waitFor(t, 20*time.Second, func() bool {
		var current wayv1.PortForwardLease
		if direct.Get(ctx, client.ObjectKeyFromObject(lease), &current) != nil {
			return false
		}
		condition := apiMeta.FindStatusCondition(current.Status.Conditions, waystatus.ConditionTargetReady)
		return condition != nil && condition.Status == metav1.ConditionFalse && current.Status.Target == nil
	})
	must(t, direct.Delete(ctx, lease))
	waitFor(t, 10*time.Second, func() bool {
		var current wayv1.PortForwardLease
		return apierrors.IsNotFound(direct.Get(ctx, client.ObjectKeyFromObject(lease), &current))
	})
	exerciseAdmissionGenerationRoll(t, direct, namespace, relativeBinary)
	stopController(t, namespace)
	plainOutage := pod("plain-outage", namespace, nil)
	must(t, direct.Create(ctx, plainOutage, client.DryRunAll))
	annotatedOutage := pod("protected-outage", namespace, map[string]string{contract.GatewayAnnotation: namespace + "/private"})
	if err = direct.Create(ctx, annotatedOutage, client.DryRunAll); err == nil {
		t.Fatal("annotated Pod was admitted while webhook was unavailable")
	}
}

func buildController(t *testing.T) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "waycloak-controller")
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"), "go", "build", "-trimpath", "-o", output, "../../cmd/controller")
	return output
}

func createInfrastructure(t *testing.T, c client.Client, namespace, deniedNamespace, roleName string) {
	t.Helper()
	ctx := context.Background()
	for _, ns := range []*corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"waycloak-e2e": "allowed"}}}, {ObjectMeta: metav1.ObjectMeta{Name: deniedNamespace}}} {
		must(t, c.Create(ctx, ns))
	}
	must(t, c.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: namespace}}))
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps", "services"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
		{APIGroups: []string{"apps"}, Resources: []string{"statefulsets"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{APIGroups: []string{"policy"}, Resources: []string{"poddisruptionbudgets"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{APIGroups: []string{"networking.waycloak.io"}, Resources: []string{"vpngateways"}, Verbs: []string{"get", "list", "watch", "update", "patch"}},
		{APIGroups: []string{"networking.waycloak.io"}, Resources: []string{"vpngateways/status", "vpnworkloads/status"}, Verbs: []string{"get", "update", "patch"}},
		{APIGroups: []string{"networking.waycloak.io"}, Resources: []string{"vpnworkloads"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{APIGroups: []string{"networking.waycloak.io"}, Resources: []string{"portforwardleases"}, Verbs: []string{"get", "list", "watch", "update", "patch"}},
		{APIGroups: []string{"networking.waycloak.io"}, Resources: []string{"portforwardleases/status"}, Verbs: []string{"get", "update", "patch"}},
		{APIGroups: []string{"networking.waycloak.io"}, Resources: []string{"vpnworkloads/finalizers"}, Verbs: []string{"update"}},
	}
	must(t, c.Create(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}, Rules: rules}))
	must(t, c.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "controller", Namespace: namespace}}}))
}

func createRunner(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	ctx := context.Background()
	must(t, c.Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "waycloak-e2e-webhook", Namespace: namespace}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "waycloak-e2e-controller"}, Ports: []corev1.ServicePort{{Name: "https", Port: 443, TargetPort: intstr.FromInt(9443)}}}}))
	createControllerRunner(t, c, namespace, "controller")
}

func createControllerRunner(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	must(t, c.Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app": "waycloak-e2e-controller"}}, Spec: corev1.PodSpec{ServiceAccountName: "controller", AutomountServiceAccountToken: boolPtr(true), NodeSelector: map[string]string{"kubernetes.io/arch": "amd64"}, Containers: []corev1.Container{{Name: "runner", Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, VolumeMounts: []corev1.VolumeMount{{Name: "certs", MountPath: "/certs", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "certs", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "webhook-certs"}}}}}}))
}

func startController(t *testing.T, namespace string, controllers bool) {
	startControllerWithImage(t, namespace, controllers, e2eAgentImage)
}

func startControllerWithImage(t *testing.T, namespace string, controllers bool, agentImage string) {
	t.Helper()
	stopController(t, namespace)
	startControllerProcess(t, namespace, "controller", controllers, agentImage, e2eAdmissionGeneration)
}

func startControllerProcess(t *testing.T, namespace, podName string, controllers bool, agentImage, generation string) {
	t.Helper()
	commandLine := fmt.Sprintf("nohup /tmp/waycloak-controller --leader-elect=false --controllers-enabled=%t --metrics-bind-address=0 --health-probe-bind-address=:8081 --webhook-cert-dir=/certs --allocation-quarantine=1s --port-forward-deletion-quarantine=1s --agent-image=%s --admission-generation=%s --admission-generation-configmap=%s --admission-generation-namespace=%s >/tmp/controller.log 2>&1 &", controllers, agentImage, generation, e2eAdmissionGenerationConfigMap, namespace)
	command(t, nil, "kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", commandLine)
	time.Sleep(2 * time.Second)
	if !commandSucceeds(namespace, podName, "pgrep waycloak-controller >/dev/null") {
		log := command(t, nil, "kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", "cat /tmp/controller.log 2>/dev/null || true")
		t.Fatalf("controller process exited during startup:\n%s", log)
	}
}

func stopController(t *testing.T, namespace string) {
	t.Helper()
	stopControllerProcess(namespace, "controller")
	time.Sleep(time.Second)
}

func stopControllerProcess(namespace, podName string) {
	_ = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", "killall waycloak-controller 2>/dev/null || true").Run()
}

func exerciseAdmissionGenerationRoll(t *testing.T, direct client.Client, namespace, relativeBinary string) {
	t.Helper()
	const nextGeneration = "2222222222222222222222222222222222222222222222222222222222222222"
	const nextAgentImage = "registry.k8s.io/pause@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	ctx := context.Background()
	createControllerRunner(t, direct, namespace, "controller-new")
	waitFor(t, 60*time.Second, func() bool {
		var runner corev1.Pod
		if direct.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "controller-new"}, &runner) != nil {
			return false
		}
		return runner.Status.Phase == corev1.PodRunning
	})
	command(t, nil, "kubectl", "cp", relativeBinary, namespace+"/controller-new:/tmp/waycloak-controller")
	command(t, nil, "kubectl", "exec", "-n", namespace, "controller-new", "--", "chmod", "+x", "/tmp/waycloak-controller")
	startControllerProcess(t, namespace, "controller-new", false, nextAgentImage, nextGeneration)
	if commandSucceeds(namespace, "controller-new", "wget -qO- http://127.0.0.1:8081/readyz >/dev/null") {
		t.Fatal("new webhook became ready before its generation was selected")
	}
	var generation corev1.ConfigMap
	must(t, direct.Get(ctx, types.NamespacedName{Namespace: namespace, Name: e2eAdmissionGenerationConfigMap}, &generation))
	generation.Data[contract.AdmissionGenerationKey] = nextGeneration
	must(t, direct.Update(ctx, &generation))
	waitFor(t, 20*time.Second, func() bool {
		return !commandSucceeds(namespace, "controller", "wget -qO- http://127.0.0.1:8081/readyz >/dev/null") && commandSucceeds(namespace, "controller-new", "wget -qO- http://127.0.0.1:8081/readyz >/dev/null")
	})
	stopControllerProcess(namespace, "controller")
	waitFor(t, 20*time.Second, func() bool {
		candidate := pod("generation-probe", namespace, map[string]string{contract.GatewayAnnotation: namespace + "/private"})
		if err := direct.Create(ctx, candidate, client.DryRunAll); err != nil {
			return false
		}
		return candidate.Annotations[contract.AdmissionGenerationAnnotation] == nextGeneration && injectedImage(candidate, contract.AgentContainer) == nextAgentImage
	})
	stopControllerProcess(namespace, "controller-new")
}

func injectedImage(pod *corev1.Pod, name string) string {
	for _, container := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		if container.Name == name {
			return container.Image
		}
	}
	return ""
}

func webhookConfigurations(mutatingName, validatingName, namespace string, ca []byte) (*admissionv1.MutatingWebhookConfiguration, *admissionv1.ValidatingWebhookConfiguration) {
	fail := admissionv1.Fail
	none := admissionv1.SideEffectClassNone
	condition := []admissionv1.MatchCondition{{Name: "opted-in", Expression: "has(object.metadata.annotations) && 'networking.waycloak.io/gateway' in object.metadata.annotations"}}
	rules := []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}}
	service := func(path string) admissionv1.WebhookClientConfig {
		port := int32(443)
		return admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Namespace: namespace, Name: "waycloak-e2e-webhook", Path: &path, Port: &port}, CABundle: ca}
	}
	m := &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: mutatingName}, Webhooks: []admissionv1.MutatingWebhook{{Name: "mpod.e2e.networking.waycloak.io", ClientConfig: service("/mutate-v1-pod"), Rules: rules, FailurePolicy: &fail, SideEffects: &none, AdmissionReviewVersions: []string{"v1"}, MatchConditions: condition}}}
	v := &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: validatingName}, Webhooks: []admissionv1.ValidatingWebhook{{Name: "vpod.e2e.networking.waycloak.io", ClientConfig: service("/validate-v1-pod"), Rules: rules, FailurePolicy: &fail, SideEffects: &none, AdmissionReviewVersions: []string{"v1"}, MatchConditions: condition}}}
	return m, v
}

func gateway(namespace string) *wayv1.VPNGateway {
	return &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: namespace}, Spec: wayv1.VPNGatewaySpec{Engine: wayv1.EngineSpec{Type: "Test"}, Provider: &wayv1.ProviderSpec{Name: "protonvpn", Protocol: "openvpn", CredentialsSecretRef: corev1.LocalObjectReference{Name: "unused"}}, Overlay: wayv1.OverlaySpec{CIDR: "172.30.99.0/29", VNI: 7999}, PortForwarding: wayv1.PortForwardingSpec{Enabled: true, Driver: "ProtonNatPmp"}, WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"waycloak-e2e": "allowed"}}}}}
}

func certificates(t *testing.T, host string) ([]byte, []byte, []byte) {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Waycloak e2e CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	must(t, err)
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	serverTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	must(t, err)
	keyBytes, err := x509.MarshalPKCS8PrivateKey(serverKey)
	must(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func pod(name, namespace string, annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: annotations}, Spec: corev1.PodSpec{AutomountServiceAccountToken: boolPtr(false), Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}}
}
func assertUnmutated(t *testing.T, p *corev1.Pod) {
	t.Helper()
	if p.Annotations[contract.InjectionVersionAnnotation] != "" || len(p.Spec.InitContainers) != 0 || len(p.Spec.Containers) != 1 || len(p.Spec.Volumes) != 0 {
		t.Fatalf("unannotated Pod was mutated: %#v", p.Spec)
	}
}
func assertAllocation(t *testing.T, c client.Client, key types.NamespacedName, want string) {
	t.Helper()
	var item wayv1.VPNWorkload
	must(t, c.Get(context.Background(), key, &item))
	if item.Status.Allocation.Address != want {
		t.Fatalf("allocation changed: %s -> %s", want, item.Status.Allocation.Address)
	}
}
func waitFor(t *testing.T, timeout time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func command(t *testing.T, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func boolPtr(v bool) *bool { return &v }
