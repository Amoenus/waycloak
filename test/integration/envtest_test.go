//go:build envtest

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestReconciliationPersistsAllocationAndConfigMap(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stdout)))
	useExisting := os.Getenv("USE_EXISTING_CLUSTER") == "1"
	if runtime.GOOS == "windows" && !useExisting {
		t.Skip("controller-runtime envtest process teardown is unsupported on Windows; use USE_EXISTING_CLUSTER=1 or Linux")
	}
	scheme := kruntime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	e := &envtest.Environment{Scheme: scheme, UseExistingCluster: &useExisting, CRDInstallOptions: envtest.CRDInstallOptions{Paths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, CleanUpAfterUse: true, MaxTime: 30 * time.Second, PollInterval: 250 * time.Millisecond}}
	cfg, err := e.Start()
	must(t, err)
	apiClient, err := client.New(cfg, client.Options{Scheme: scheme})
	must(t, err)
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: server.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false})
	must(t, err)
	recorder := record.NewFakeRecorder(100)
	must(t, (&waycontroller.PodReconciler{Client: mgr.GetClient(), Scheme: scheme, Recorder: recorder}).SetupWithManager(mgr))
	must(t, (&waycontroller.GatewayReconciler{Client: mgr.GetClient(), Scheme: scheme, Recorder: recorder, ManagerImage: "registry.invalid/manager@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}).SetupWithManager(mgr))
	must(t, (&waycontroller.WorkloadGCReconciler{Client: mgr.GetClient(), Quarantine: time.Second}).SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				t.Errorf("manager: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("manager did not stop")
		}
	}()

	nsName := "waycloak-envtest-" + fmt.Sprint(time.Now().UnixNano())
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	must(t, mgr.GetClient().Create(ctx, ns))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		var list wayv1.VPNWorkloadList
		if err := apiClient.List(cleanupCtx, &list, client.InNamespace(nsName)); err == nil {
			for i := range list.Items {
				item := &list.Items[i]
				item.Finalizers = nil
				_ = apiClient.Update(cleanupCtx, item)
				_ = apiClient.Delete(cleanupCtx, item)
			}
		}
		var gateways wayv1.VPNGatewayList
		if err := apiClient.List(cleanupCtx, &gateways, client.InNamespace(nsName)); err == nil {
			for i := range gateways.Items {
				_ = apiClient.Delete(cleanupCtx, &gateways.Items[i])
			}
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var remainingWorkloads wayv1.VPNWorkloadList
			var remainingGateways wayv1.VPNGatewayList
			workloadsGone := apiClient.List(cleanupCtx, &remainingWorkloads, client.InNamespace(nsName)) != nil || len(remainingWorkloads.Items) == 0
			gatewaysGone := apiClient.List(cleanupCtx, &remainingGateways, client.InNamespace(nsName)) != nil || len(remainingGateways.Items) == 0
			if workloadsGone && gatewaysGone {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		_ = apiClient.Delete(cleanupCtx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})
	})
	must(t, mgr.GetClient().Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: nsName}, StringData: map[string]string{"test": "not-a-real-credential"}}))
	gw := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: nsName}, Spec: wayv1.VPNGatewaySpec{Engine: wayv1.EngineSpec{Type: "Test", Image: "registry.invalid/engine@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, Provider: wayv1.ProviderSpec{Name: "test", CredentialsSecretRef: corev1.LocalObjectReference{Name: "credentials"}}, Overlay: wayv1.OverlaySpec{CIDR: "172.30.99.0/29", VNI: 7999, MTU: 1320}, WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: metav1.LabelSelector{}}}}
	must(t, mgr.GetClient().Create(ctx, gw))
	waitFor(t, 10*time.Second, func() bool {
		var statefulSet appsv1.StatefulSet
		return mgr.GetClient().Get(ctx, types.NamespacedName{Namespace: nsName, Name: waygateway.ResourceName(gw.Name)}, &statefulSet) == nil
	})
	var service corev1.Service
	must(t, mgr.GetClient().Get(ctx, types.NamespacedName{Namespace: nsName, Name: waygateway.ResourceName(gw.Name)}, &service))
	if service.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("gateway Service clusterIP = %q", service.Spec.ClusterIP)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: nsName, Annotations: map[string]string{contract.GatewayAnnotation: "private", contract.InjectionVersionAnnotation: contract.InjectionVersion}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}}
	must(t, mgr.GetClient().Create(ctx, pod))
	cmKey := types.NamespacedName{Namespace: nsName, Name: contract.AllocationConfigMapName(nsName, pod.Name)}
	var cm corev1.ConfigMap
	waitFor(t, 20*time.Second, func() bool { return mgr.GetClient().Get(ctx, cmKey, &cm) == nil })
	if cm.Data["podUID"] != string(pod.UID) {
		t.Fatalf("ConfigMap UID=%q Pod UID=%q", cm.Data["podUID"], pod.UID)
	}
	var workloads wayv1.VPNWorkloadList
	must(t, mgr.GetClient().List(ctx, &workloads, client.InNamespace(nsName)))
	if len(workloads.Items) != 1 || workloads.Items[0].Status.Allocation.Address != "172.30.99.2" {
		t.Fatalf("unexpected workloads: %#v", workloads.Items)
	}
	workload := &workloads.Items[0]
	workload.Finalizers = nil
	must(t, mgr.GetClient().Update(ctx, workload))
	must(t, mgr.GetClient().Delete(ctx, pod))
	must(t, mgr.GetClient().Delete(ctx, workload))
	_ = mgr.GetClient().Delete(ctx, &cm)
	waitFor(t, 10*time.Second, func() bool {
		var item wayv1.VPNWorkload
		return apierrors.IsNotFound(mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(workload), &item))
	})
	must(t, apiClient.Delete(ctx, gw))
	waitFor(t, 10*time.Second, func() bool {
		var item wayv1.VPNGateway
		return apierrors.IsNotFound(apiClient.Get(ctx, client.ObjectKeyFromObject(gw), &item))
	})
	must(t, apiClient.Delete(ctx, ns))
}

func waitFor(t *testing.T, timeout time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
