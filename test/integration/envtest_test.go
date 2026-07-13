//go:build envtest

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"github.com/Amoenus/waycloak/internal/delivery"
	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	"github.com/Amoenus/waycloak/internal/provider"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
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
	must(t, policyv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	e := &envtest.Environment{Scheme: scheme, UseExistingCluster: &useExisting, CRDInstallOptions: envtest.CRDInstallOptions{Paths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, CleanUpAfterUse: !useExisting, MaxTime: 30 * time.Second, PollInterval: 250 * time.Millisecond}}
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
	leaseObserver := &integrationLeaseObserver{issuedAt: time.Now().UTC()}
	must(t, (&waycontroller.PortForwardLeaseReconciler{Client: mgr.GetClient(), Recorder: recorder, Observer: leaseObserver, DeliveryObserver: &integrationDeliveryObserver{client: mgr.GetClient()}, DeletionQuarantine: time.Second}).SetupWithManager(mgr))
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
	gw := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: nsName}, Spec: wayv1.VPNGatewaySpec{Engine: wayv1.EngineSpec{Type: "Test", Image: "registry.invalid/engine@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, Provider: wayv1.ProviderSpec{Name: "protonvpn", Protocol: "openvpn", CredentialsSecretRef: corev1.LocalObjectReference{Name: "credentials"}}, Overlay: wayv1.OverlaySpec{CIDR: "172.30.99.0/29", VNI: 7999, MTU: 1320}, PortForwarding: wayv1.PortForwardingSpec{Enabled: true, Driver: "ProtonNatPmp"}, WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: metav1.LabelSelector{}}}}
	must(t, mgr.GetClient().Create(ctx, gw))
	invalidGateway := gw.DeepCopy()
	invalidGateway.Name = "invalid-provider"
	invalidGateway.ResourceVersion = ""
	invalidGateway.UID = ""
	invalidGateway.Spec.Provider.Name = "unsupported"
	if err := mgr.GetClient().Create(ctx, invalidGateway); err == nil {
		t.Fatal("API server accepted ProtonNatPmp with an unsupported provider")
	}
	waitFor(t, 10*time.Second, func() bool {
		var statefulSet appsv1.StatefulSet
		return mgr.GetClient().Get(ctx, types.NamespacedName{Namespace: nsName, Name: waygateway.ResourceName(gw.Name)}, &statefulSet) == nil
	})
	var service corev1.Service
	must(t, mgr.GetClient().Get(ctx, types.NamespacedName{Namespace: nsName, Name: waygateway.ResourceName(gw.Name)}, &service))
	if service.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("gateway Service clusterIP = %q", service.Spec.ClusterIP)
	}
	var gatewayStatefulSet appsv1.StatefulSet
	must(t, apiClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: waygateway.ResourceName(gw.Name)}, &gatewayStatefulSet))
	yes := true
	observerPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gateway-observer", Namespace: nsName, Labels: waygateway.SelectorLabels(gw), OwnerReferences: []metav1.OwnerReference{{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "StatefulSet", Name: gatewayStatefulSet.Name, UID: gatewayStatefulSet.UID, Controller: &yes}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "fixture", Image: "registry.invalid/fixture"}}}}
	must(t, apiClient.Create(ctx, observerPod))
	must(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := apiClient.Get(ctx, client.ObjectKeyFromObject(observerPod), observerPod); err != nil {
			return err
		}
		observerPod.Status.PodIP = "127.0.0.1"
		return apiClient.Status().Update(ctx, observerPod)
	}))
	var pdb policyv1.PodDisruptionBudget
	must(t, mgr.GetClient().Get(ctx, types.NamespacedName{Namespace: nsName, Name: waygateway.ResourceName(gw.Name)}, &pdb))
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 1 {
		t.Fatalf("gateway disruption budget = %#v", pdb.Spec)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: nsName, Labels: map[string]string{"app": "protected"}, Annotations: map[string]string{contract.GatewayAnnotation: "private", contract.InjectionVersionAnnotation: contract.InjectionVersion}}, Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "app", Image: "registry.k8s.io/pause:3.10.1"},
		{Name: contract.AgentContainer, Image: "registry.k8s.io/pause:3.10.1"},
		{Name: "provider-port-adapter", Image: "registry.k8s.io/pause:3.10.1", ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(1)}}, PeriodSeconds: 1}},
	}}}
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
	var observedPod corev1.Pod
	must(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := apiClient.Get(ctx, client.ObjectKeyFromObject(pod), &observedPod); err != nil {
			return err
		}
		observedPod.Status.PodIP = "127.0.0.2"
		observedPod.Status.Phase = corev1.PodRunning
		observedPod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
		observedPod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: contract.AgentContainer, Ready: true}}
		return apiClient.Status().Update(ctx, &observedPod)
	}))
	waitFor(t, 30*time.Second, func() bool {
		if apiClient.Get(ctx, client.ObjectKeyFromObject(pod), &observedPod) != nil || observedPod.Status.Phase != corev1.PodRunning || observedPod.Status.PodIP == "" {
			return false
		}
		agentReady := false
		for _, status := range observedPod.Status.ContainerStatuses {
			if status.Name == contract.AgentContainer {
				agentReady = status.Ready
			}
		}
		return agentReady && !podConditionReady(&observedPod)
	})
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: nsName}, Spec: wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedNameReference{Name: gw.Name}, Target: wayv1.PortForwardTargetSpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "protected"}}, Port: 6881, ApplicationPortMode: delivery.ApplicationPortModeProviderAssigned}, Protocols: []wayv1.PortForwardProtocol{wayv1.PortForwardProtocolTCP, wayv1.PortForwardProtocolUDP}}}
	must(t, mgr.GetClient().Create(ctx, lease))
	var currentLease wayv1.PortForwardLease
	leaseReady := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(lease), &currentLease) != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		target := apiMeta.FindStatusCondition(currentLease.Status.Conditions, waystatus.ConditionTargetReady)
		provider := apiMeta.FindStatusCondition(currentLease.Status.Conditions, waystatus.ConditionProviderLeaseReady)
		rules := apiMeta.FindStatusCondition(currentLease.Status.Conditions, waystatus.ConditionGatewayRulesReady)
		delivered := apiMeta.FindStatusCondition(currentLease.Status.Conditions, waystatus.ConditionDelivered)
		ready := apiMeta.FindStatusCondition(currentLease.Status.Conditions, waystatus.ConditionReady)
		leaseReady = target != nil && target.Status == metav1.ConditionTrue && provider != nil && provider.Status == metav1.ConditionTrue && provider.Reason == waystatus.ReasonProviderLeaseObservedReady && rules != nil && rules.Status == metav1.ConditionTrue && rules.Reason == waystatus.ReasonGatewayRulesObservedReady && delivered != nil && delivered.Status == metav1.ConditionTrue && delivered.Reason == waystatus.ReasonDeliveryObservedReady && ready != nil && ready.Status == metav1.ConditionTrue && ready.Reason == waystatus.ReasonLeaseReady && currentLease.Status.Target != nil && currentLease.Status.Target.PodRef.UID == pod.UID && currentLease.Status.PublicPort == 42000 && currentLease.Status.LeaseGeneration == 1
		if leaseReady {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !leaseReady {
		must(t, apiClient.Get(ctx, client.ObjectKeyFromObject(pod), &observedPod))
		t.Fatalf("provider-assigned lease did not become ready: status=%#v podStatus=%#v", currentLease.Status, observedPod.Status)
	}
	must(t, mgr.GetClient().Get(ctx, cmKey, &cm))
	var document delivery.Document
	must(t, json.Unmarshal([]byte(cm.Data[contract.PortForwardLeasesKey]), &document))
	if document.PodUID != string(pod.UID) || len(document.Leases) != 1 || document.Leases[0].Identity != string(lease.UID) || document.Leases[0].Generation != 1 || document.Leases[0].ApplicationPortMode != delivery.ApplicationPortModeProviderAssigned || document.Leases[0].ApplicationPort != 42000 {
		t.Fatalf("delivered document = %#v", document)
	}
	must(t, mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(pod), &observedPod))
	if observedPod.Annotations[contract.DeliveryDigestAnnotation] != contract.DeliveryDigest(cm.Data[contract.PortForwardLeasesKey]) {
		t.Fatalf("delivery projection digest = %q", observedPod.Annotations[contract.DeliveryDigestAnnotation])
	}
	invalidLease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: nsName}, Spec: wayv1.PortForwardLeaseSpec{GatewayRef: wayv1.NamespacedNameReference{Name: gw.Name}, Target: wayv1.PortForwardTargetSpec{PodSelector: metav1.LabelSelector{}, Port: 6881}, Protocols: []wayv1.PortForwardProtocol{wayv1.PortForwardProtocolTCP}}}
	if err := mgr.GetClient().Create(ctx, invalidLease); err == nil {
		t.Fatal("API server accepted an empty port-forward target selector")
	}
	must(t, mgr.GetClient().Delete(ctx, pod))
	waitFor(t, 10*time.Second, func() bool {
		var item corev1.Pod
		return apierrors.IsNotFound(apiClient.Get(ctx, client.ObjectKeyFromObject(pod), &item))
	})
	var retiring wayv1.VPNWorkload
	if err := apiClient.Get(ctx, client.ObjectKeyFromObject(workload), &retiring); err == nil {
		if retiring.DeletionTimestamp.IsZero() {
			must(t, apiClient.Delete(ctx, &retiring))
		}
	} else if !apierrors.IsNotFound(err) {
		must(t, err)
	}
	waitFor(t, 10*time.Second, func() bool {
		var item wayv1.VPNWorkload
		return apierrors.IsNotFound(apiClient.Get(ctx, client.ObjectKeyFromObject(workload), &item))
	})
	waitFor(t, 10*time.Second, func() bool {
		var current wayv1.PortForwardLease
		if mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(lease), &current) != nil {
			return false
		}
		condition := apiMeta.FindStatusCondition(current.Status.Conditions, waystatus.ConditionTargetReady)
		return condition != nil && condition.Status == metav1.ConditionFalse && current.Status.Target == nil
	})
	must(t, mgr.GetClient().Delete(ctx, lease))
	waitFor(t, 30*time.Second, func() bool {
		var current wayv1.PortForwardLease
		return apierrors.IsNotFound(apiClient.Get(ctx, client.ObjectKeyFromObject(lease), &current))
	})
	must(t, apiClient.Delete(ctx, gw))
	waitFor(t, 10*time.Second, func() bool {
		var item wayv1.VPNGateway
		return apierrors.IsNotFound(apiClient.Get(ctx, client.ObjectKeyFromObject(gw), &item))
	})
	must(t, apiClient.Delete(ctx, ns))
}

type integrationLeaseObserver struct {
	issuedAt time.Time
}

func (observer *integrationLeaseObserver) ObserveLease(_ context.Context, _ string, identity string) (waygateway.PortForwardObservation, error) {
	return waygateway.PortForwardObservation{Identity: identity, InternalPort: 49152, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}, PublicPort: 42000, IssuedAt: observer.issuedAt, RenewAfter: observer.issuedAt.Add(10 * time.Second), ExpiresAt: observer.issuedAt.Add(20 * time.Second), Ready: true, GatewayRulesReady: true, GatewayRulesGeneration: 1, TargetAddress: "172.30.99.2", TargetPort: 6881}, nil
}

type integrationDeliveryObserver struct {
	client client.Client
}

func (observer *integrationDeliveryObserver) ObserveDelivery(ctx context.Context, podIP, identity string) (delivery.Observation, error) {
	var pods corev1.PodList
	if err := observer.client.List(ctx, &pods); err != nil {
		return delivery.Observation{}, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.PodIP != podIP {
			continue
		}
		var cm corev1.ConfigMap
		if err := observer.client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: contract.AllocationConfigMapName(pod.Namespace, pod.Name)}, &cm); err != nil {
			return delivery.Observation{}, err
		}
		var document delivery.Document
		if err := json.Unmarshal([]byte(cm.Data[contract.PortForwardLeasesKey]), &document); err != nil {
			return delivery.Observation{}, err
		}
		if err := document.Validate(time.Now().UTC()); err != nil {
			return delivery.Observation{}, err
		}
		for _, record := range document.Leases {
			if record.Identity == identity {
				return delivery.Observation{APIVersion: document.APIVersion, Identity: identity, PodUID: document.PodUID, Generation: record.Generation, ExpiresAt: record.ExpiresAt, Ready: true, AppliedPort: record.ApplicationPort}, nil
			}
		}
	}
	return delivery.Observation{}, delivery.ErrRecordNotFound
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

func podConditionReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
