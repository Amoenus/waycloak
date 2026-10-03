// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package installation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	installv1 "github.com/Amoenus/waycloak/api/installation/v1alpha1"
	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type interruptedBackend struct {
	*Backend
	interrupt map[string]bool
}

func (b *interruptedBackend) Advance(ctx context.Context, uid types.UID, snapshot waycloakctl.NativeInstallPlan, phase string) (string, error) {
	next, err := b.Backend.Advance(ctx, uid, snapshot, phase)
	if err == nil && b.interrupt[phase] {
		delete(b.interrupt, phase)
		return phase, errors.New("test: process interrupted after phase mutation before checkpoint persistence")
	}
	return next, err
}

// This test uses locally built, digest-pinned fixture artifacts in the disposable
// turnkey cluster. Only this test preloads the private verified/artifact caches;
// publication signature verification has separate real-bundle integration tests.
// No executable is available to the reconciliation path.
func TestNativeLifecycleInDisposableKind(t *testing.T) {
	if os.Getenv("WAYCLOAK_NATIVE_E2E") != "1" {
		t.Skip("requires disposable turnkey Kind fixture")
	}
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, &clientcmd.ConfigOverrides{})
	raw, err := configLoader.RawConfig()
	if err != nil {
		t.Fatal(err)
	}
	if raw.CurrentContext != "kind-waycloak-turnkey-ci" {
		t.Fatal("refusing native lifecycle test outside the disposable turnkey context")
	}
	config, err := configLoader.ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.QPS, config.Burst = 40, 80
	clients, err := waycloakctl.NewClientsForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := installv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(os.Getenv("WAYCLOAK_NATIVE_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := waycloakctl.DecodeReleaseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loader.Load(os.Getenv("WAYCLOAK_NATIVE_CHART"))
	if err != nil {
		t.Fatal(err)
	}
	chart, ok := loaded.(*chartv2.Chart)
	if !ok {
		t.Fatal("expected v2 chart")
	}
	versions := map[string]waycloakctl.ReleaseManifest{}
	for _, suffix := range []string{"-native-a", "-native-b"} {
		manifest := base
		manifest.Version = base.Version + suffix
		manifest.Chart.Repository = "oci://ghcr.io/amoenus/charts/waycloak"
		manifest.ManifestDigest, err = manifest.IdentityDigest()
		if err != nil {
			t.Fatal(err)
		}
		versions[manifest.Version] = manifest
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	protected, ordinary := nativeProbePods(t, ctx, clients)
	probeURL := os.Getenv("WAYCLOAK_NATIVE_PROBE_URL")
	ordinaryIP, err := nativeExecProbe(ctx, config, clients, ordinary, probeURL)
	if err != nil || ordinaryIP == "" {
		t.Fatal("ordinary positive control failed", err)
	}
	vpnIP, err := nativeExecProbe(ctx, config, clients, protected, probeURL)
	if err != nil || vpnIP == "" || vpnIP == ordinaryIP {
		t.Fatal("protected positive control did not use distinct egress", err)
	}
	t.Setenv("PATH", "")
	var lock sync.Mutex
	var succeeded, denied, fallback, collection int
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for monitorCtx.Err() == nil {
			observed, err := nativeExecProbe(monitorCtx, config, clients, protected, probeURL)
			if monitorCtx.Err() != nil {
				return
			}
			lock.Lock()
			if err == nil {
				succeeded++
				if observed == ordinaryIP || observed == "" {
					fallback++
				}
			} else {
				var exit interface{ ExitStatus() int }
				if errors.As(err, &exit) {
					denied++
				} else {
					collection++
				}
			}
			lock.Unlock()
			select {
			case <-monitorCtx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() { stopMonitor(); <-done })
	installation := &installv1.WaycloakInstallation{ObjectMeta: metav1.ObjectMeta{Name: "waycloak"}, Spec: installv1.WaycloakInstallationSpec{Version: base.Version + "-native-a", Namespace: "waycloak-system", Release: "waycloak", OverlayCIDR: "100.96.0.0/16", AdoptExisting: true, Config: installv1.InstallationConfig{PortForwarding: &installv1.PortForwarding{ControllerTLSSecret: "waycloak-port-forward-controller-tls", AdapterEnabled: true}}}}
	if err := api.Create(ctx, installation); err != nil {
		t.Fatal(err)
	}
	interrupt := map[string]bool{}
	for _, phase := range testPhases[:len(testPhases)-1] {
		interrupt[phase] = true
	}
	for round, target := range []string{base.Version + "-native-a", base.Version + "-native-a", base.Version + "-native-b", base.Version + "-native-a"} {
		if round > 0 {
			if err := api.Get(ctx, client.ObjectKey{Name: "waycloak"}, installation); err != nil {
				t.Fatal(err)
			}
			installation.Spec.Version = target
			if round == 1 {
				installation.Spec.Config.ControllerNodeSelector = map[string]string{"kubernetes.io/os": "linux"}
			}
			if err := api.Update(ctx, installation); err != nil {
				t.Fatal(err)
			}
		}
		complete := false
		for attempt := 0; attempt < 240; attempt++ {
			// Reconstruct every process-local object; only API journals and Helm
			// Secrets survive this simulated executor restart.
			actions := new(action.Configuration)
			if err := actions.Init(&restGetter{config: config, namespace: "waycloak-system"}, "waycloak-system", "secret"); err != nil {
				t.Fatal(err)
			}
			cache := map[waycloakctl.Artifact]*chartv2.Chart{}
			for _, manifest := range versions {
				cache[manifest.Chart] = chart
			}
			backend := &interruptedBackend{Backend: &Backend{Clients: clients, Runtime: &HelmRuntime{Configuration: actions, Namespace: "waycloak-system", charts: cache}, verified: versions}, interrupt: interrupt}
			r := &Reconciler{Client: api, Clients: clients, Namespace: "waycloak-system", Backend: backend}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "waycloak"}}); err != nil {
				t.Fatal(err)
			}
			if err := api.Get(ctx, client.ObjectKey{Name: "waycloak"}, installation); err != nil {
				t.Fatal(err)
			}
			if installation.Status.Phase == "Ready" && installation.Status.ReadyGeneration == installation.Generation {
				complete = true
				break
			}
			for _, condition := range installation.Status.Conditions {
				if condition.Reason == "ReconciliationFailed" && !strings.Contains(condition.Message, "test: process interrupted") {
					t.Fatalf("native reconciliation failed at %s: %s", installation.Status.Phase, condition.Message)
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
		if !complete {
			t.Fatal("native transition did not reach observed readiness")
		}
		observed, err := nativeExecProbe(ctx, config, clients, protected, probeURL)
		if err != nil || observed == ordinaryIP || observed == "" {
			t.Fatal("protected egress did not recover", err)
		}
		t.Logf("round %d reached Ready for generation %d", round, installation.Generation)
	}
	if len(interrupt) != 0 {
		t.Fatalf("unexercised interruption phases: %v", interrupt)
	}
	nativeRecoveryWithoutAgentsOrDNS(t, ctx, clients)
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		observed, err := nativeExecProbe(ctx, config, clients, protected, probeURL)
		return err == nil && observed != "" && observed != ordinaryIP, nil
	}); err != nil {
		t.Fatal("protected egress did not recover after control-plane bootstrap test", err)
	}
	stopMonitor()
	<-done
	lock.Lock()
	defer lock.Unlock()
	t.Logf("native lifecycle packet samples: VPN=%d denied=%d ordinary-fallback=%d collection-errors=%d", succeeded, denied, fallback, collection)
	if succeeded == 0 || denied == 0 || fallback != 0 || collection != 0 {
		t.Fatal("native lifecycle packet qualification failed")
	}
}

func nativeRecoveryWithoutAgentsOrDNS(t *testing.T, ctx context.Context, clients *waycloakctl.Clients) {
	t.Helper()
	agents := clients.Kubernetes.AppsV1().DaemonSets("waycloak-system")
	ds, err := agents.Get(ctx, "waycloak-node-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	selector := ds.Spec.Template.Spec.NodeSelector
	ds.Spec.Template.Spec.NodeSelector = map[string]string{"waycloak-test-agent-disabled": "true"}
	if _, err := agents.Update(ctx, ds, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		current, err := agents.Get(ctx, ds.Name, metav1.GetOptions{})
		if err == nil {
			current.Spec.Template.Spec.NodeSelector = selector
			_, err = agents.Update(ctx, current, metav1.UpdateOptions{})
		}
		if err != nil {
			t.Error("restore node agents", err)
		}
	}()
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		pods, err := clients.Kubernetes.CoreV1().Pods("waycloak-system").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=node-agent"})
		return err == nil && len(pods.Items) == 0, err
	}); err != nil {
		t.Fatal("withdraw test agents", err)
	}
	dnsClient := clients.Kubernetes.AppsV1().Deployments("kube-system")
	dns, err := dnsClient.Get(ctx, "coredns", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	replicas := dns.Spec.Replicas
	zero := int32(0)
	dns.Spec.Replicas = &zero
	if _, err := dnsClient.Update(ctx, dns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		current, err := dnsClient.Get(ctx, dns.Name, metav1.GetOptions{})
		if err == nil {
			current.Spec.Replicas = replicas
			_, err = dnsClient.Update(ctx, current, metav1.UpdateOptions{})
		}
		if err != nil {
			t.Error("restore cluster DNS", err)
		}
	}()
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		pods, err := clients.Kubernetes.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=kube-dns"})
		return err == nil && len(pods.Items) == 0, err
	}); err != nil {
		t.Fatal("withdraw cluster DNS", err)
	}
	for _, component := range []string{"controller", "cni-installer"} {
		podsClient := clients.Kubernetes.CoreV1().Pods("waycloak-system")
		labels := "app.kubernetes.io/component=" + component
		pods, err := podsClient.List(ctx, metav1.ListOptions{LabelSelector: labels})
		if err != nil || len(pods.Items) == 0 {
			t.Fatal("find recovery component", component, err)
		}
		old := map[types.UID]bool{}
		for _, pod := range pods.Items {
			old[pod.UID] = true
			uid := pod.UID
			if err := podsClient.Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			current, err := podsClient.List(ctx, metav1.ListOptions{LabelSelector: labels})
			if err != nil || len(current.Items) != len(old) {
				return false, err
			}
			for _, pod := range current.Items {
				if old[pod.UID] || !pod.Spec.HostNetwork || pod.DeletionTimestamp != nil {
					return false, nil
				}
				ready := false
				for _, condition := range pod.Status.Conditions {
					ready = ready || condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
				}
				if !ready {
					return false, nil
				}
			}
			return true, nil
		}); err != nil {
			t.Fatal("component could not restart without agents or DNS", component, err)
		}
	}
	t.Log("runtime controller and CNI installers restarted with all agents and cluster DNS unavailable")
}

func nativeExecProbe(ctx context.Context, config *rest.Config, clients *waycloakctl.Clients, pod, url string) (string, error) {
	request := clients.Kubernetes.CoreV1().RESTClient().Post().Namespace("waycloak-smoke").Resource("pods").Name(pod).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "probe", Command: []string{"curl", "--silent", "--show-error", "--fail", "--max-time", "2", "--connect-timeout", "1", "--cacert", "/observer-ca/ca.crt", url}, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", request.URL())
	if err != nil {
		return "", err
	}
	var output bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &output, Stderr: io.Discard})
	return strings.TrimSpace(output.String()), err
}

func nativeProbePods(t *testing.T, ctx context.Context, clients *waycloakctl.Clients) (string, string) {
	t.Helper()
	routes := schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpnegressroutes"}
	route := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.waycloak.io/v1beta1", "kind": "VPNEgressRoute", "metadata": map[string]any{"name": "native-guard", "namespace": "waycloak-smoke"}, "spec": map[string]any{"parentRefs": []any{map[string]any{"group": "networking.waycloak.io", "kind": "VPNGateway", "namespace": "waycloak-smoke", "name": "disposable"}}, "requiredFeatures": []any{"networking.waycloak.io/TCP"}}}}
	if _, err := clients.Dynamic.Resource(routes).Namespace("waycloak-smoke").Create(ctx, route, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"native-protected", "native-ordinary"} {
		labels := map[string]string{}
		if name == "native-protected" {
			labels["networking.waycloak.io/egress-route"] = "native-guard"
		}
		no := false
		yes := true
		uid := int64(1000)
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "waycloak-smoke", Labels: labels}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "probe", Image: os.Getenv("WAYCLOAK_NATIVE_PROBE_IMAGE"), Command: []string{"sleep", "1200"}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, VolumeMounts: []corev1.VolumeMount{{Name: "observer-ca", MountPath: "/observer-ca", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "observer-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "observer-ca"}}}}}}}
		if _, err := clients.Kubernetes.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			current, err := clients.Kubernetes.CoreV1().Pods(pod.Namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			for _, condition := range current.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					return true, nil
				}
			}
			return false, nil
		}); err != nil {
			t.Fatal(fmt.Errorf("probe %s: %w", name, err))
		}
	}
	return "native-protected", "native-ordinary"
}
