// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"github.com/Amoenus/waycloak/internal/enrollment"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestReplacementAPIFreshInstall(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_REPLACEMENT_API") != "1" {
		t.Skip("set WAYCLOAK_E2E_REPLACEMENT_API=1 to install and verify the replacement API")
	}
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	namespace := "waycloak-api-" + suffix
	release := "waycloak-api-" + suffix
	className := "e2e-" + suffix + ".waycloak.test"
	chartPath := filepath.Join("..", "..", "charts", "waycloak")

	command(t, nil, "kubectl", "create", "namespace", namespace)
	t.Cleanup(func() {
		_ = exec.Command("helm", "uninstall", release, "--namespace", namespace).Run()
		_ = exec.Command("kubectl", "delete", "vpngatewayclass", className, "--ignore-not-found", "--wait=true", "--timeout=30s").Run()
		_ = exec.Command("kubectl", "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=30s").Run()
	})

	command(t, nil, "helm", "upgrade", "--install", release, chartPath,
		"--namespace", namespace,
		"--set", "defaultGatewayClass.enabled=true",
		"--set", "defaultGatewayClass.releaseIdentity.version=v1.0.0-beta.1",
		"--set", "defaultGatewayClass.releaseIdentity.manifestDigest=sha256:4444444444444444444444444444444444444444444444444444444444444444",
		"--wait", "--timeout", "3m")

	wantResources := []string{
		"portforwardleases.networking.waycloak.io",
		"vpnegressroutes.networking.waycloak.io",
		"vpngatewayclasses.networking.waycloak.io",
		"vpngateways.networking.waycloak.io",
		"vpnworkloadbindings.networking.waycloak.io",
		"workloadadapters.networking.waycloak.io",
	}
	gotResources := strings.Fields(command(t, nil, "kubectl", "api-resources", "--api-group=networking.waycloak.io", "-o", "name"))
	sort.Strings(gotResources)
	if strings.Join(gotResources, "\n") != strings.Join(wantResources, "\n") {
		t.Fatalf("replacement discovery resources = %q, want %q", gotResources, wantResources)
	}
	assertCommandFails(t, "v1alpha1 discovery remained served", nil, "kubectl", "get", "--raw", "/apis/networking.waycloak.io/v1alpha1")
	assertCommandFails(t, "removed VPNWorkload remained discoverable", nil, "kubectl", "get", "vpnworkloads.networking.waycloak.io", "-A")

	label := "app.kubernetes.io/instance=" + release
	if output := strings.TrimSpace(command(t, nil, "kubectl", "get", "deployments", "-n", namespace, "-l", label, "-o", "name")); output != "" {
		t.Fatalf("API-only chart installed a runtime Deployment: %s", output)
	}
	for _, resource := range []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"} {
		if output := strings.TrimSpace(command(t, nil, "kubectl", "get", resource, "-l", label, "-o", "name")); output != "" {
			t.Fatalf("API-only chart installed %s: %s", resource, output)
		}
	}
	for _, resource := range []string{"validatingadmissionpolicy", "validatingadmissionpolicybinding"} {
		for _, suffix := range []string{"binding-guard", "pod-enrollment"} {
			command(t, nil, "kubectl", "get", resource, release+"-"+suffix)
		}
	}
	command(t, nil, "kubectl", "get", "clusterrole", release)
	command(t, nil, "kubectl", "get", "vpngatewayclass", "gluetun.waycloak.io")
	for _, role := range []string{
		"waycloak-distribution",
		"waycloak-network-operator",
		"waycloak-workload-owner",
		"waycloak-adapter-operator",
		"waycloak-node-agent",
		"waycloak-gateway-secret-reader",
	} {
		command(t, nil, "kubectl", "get", "clusterrole", role)
	}

	class := fmt.Sprintf(`apiVersion: networking.waycloak.io/v1beta1
kind: VPNGatewayClass
metadata:
  name: %s
spec:
  controllerName: e2e.waycloak.test/controller
  releaseIdentity:
    version: v1.0.0-beta.1
    manifestDigest: sha256:1111111111111111111111111111111111111111111111111111111111111111
  supportedFeatures:
    - networking.waycloak.io/CoreFailClosedEgress
    - networking.waycloak.io/TCP
    - networking.waycloak.io/UDP
    - networking.waycloak.io/DNSContainment
    - networking.waycloak.io/GatewayReplacementRecovery
    - networking.waycloak.io/NodeRestartRecovery
  conformanceProfile: networking.waycloak.io/Core-v1
`, className)
	applyInput(t, nil, class)

	gatewayAndRoute := fmt.Sprintf(`apiVersion: networking.waycloak.io/v1beta1
kind: VPNGateway
metadata:
  name: private
  namespace: %s
spec:
  gatewayClassName: %s
  requestedFeatures:
    - networking.waycloak.io/CoreFailClosedEgress
  clusterTraffic:
    mode: TunnelAll
  dns:
    mode: Gateway
---
apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: private
  namespace: %s
spec:
  parentRefs:
    - group: networking.waycloak.io
      kind: VPNGateway
      namespace: %s
      name: private
`, namespace, className, namespace, namespace)
	applyInput(t, nil, gatewayAndRoute)
	verifyGatewayClassContract(t, namespace, className)
	patch := `{"status":{"observedGeneration":1,"supportedFeatures":["networking.waycloak.io/CoreFailClosedEgress","networking.waycloak.io/TCP","networking.waycloak.io/UDP","networking.waycloak.io/DNSContainment","networking.waycloak.io/GatewayReplacementRecovery","networking.waycloak.io/NodeRestartRecovery"],"addresses":[{"type":"networking.waycloak.io/OverlayCIDR","value":"198.51.100.0/29"},{"type":"networking.waycloak.io/OverlayAddress","value":"198.51.100.1"},{"type":"networking.waycloak.io/UnderlayEndpoint","value":"203.0.113.10:4789"},{"type":"networking.waycloak.io/OverlayHealthPort","value":"18080"},{"type":"networking.waycloak.io/VNI","value":"7999"},{"type":"networking.waycloak.io/MTU","value":"1320"}],"conditions":[{"type":"Accepted","status":"True","reason":"Accepted","message":"Gateway intent is accepted","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"},{"type":"Programmed","status":"True","reason":"Programmed","message":"Gateway is programmed","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"},{"type":"Ready","status":"True","reason":"Ready","message":"Gateway data plane is ready","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"}]}}`
	command(t, nil, "kubectl", "patch", "vpngateway", "private", "-n", namespace, "--subresource=status", "--type=merge", "-p", patch)
	componentPatch := `{"status":{"conditions":[{"type":"Accepted","status":"True","reason":"Accepted","message":"Gateway intent is accepted","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"},{"type":"Programmed","status":"True","reason":"Programmed","message":"Gateway is programmed","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"},{"type":"Ready","status":"True","reason":"Ready","message":"Gateway data plane is ready","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"},{"type":"TunnelReady","status":"True","reason":"TunnelReady","message":"Tunnel is observed","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"},{"type":"DNSReady","status":"True","reason":"DNSReady","message":"DNS is observed","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"},{"type":"MembershipApplied","status":"True","reason":"MembershipApplied","message":"Membership is observed","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"}]}}`
	command(t, nil, "kubectl", "patch", "vpngateway", "private", "-n", namespace, "--subresource=status", "--type=merge", "-p", componentPatch)
	invalidComponentPatch := `{"status":{"conditions":[{"type":"TunnelReady","status":"True","reason":"Ready","message":"invalid reason","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"}]}}`
	assertCommandFails(t, "API server accepted an unstable gateway component reason", nil, "kubectl", "patch", "vpngateway", "private", "-n", namespace, "--subresource=status", "--type=merge", "--dry-run=server", "-p", invalidComponentPatch)
	wrongKindConditionPatch := `{"status":{"conditions":[{"type":"TunnelReady","status":"True","reason":"TunnelReady","message":"wrong kind","observedGeneration":1,"lastTransitionTime":"2026-07-26T12:00:00Z"}]}}`
	assertCommandFails(t, "API server accepted a gateway component condition on VPNEgressRoute", nil, "kubectl", "patch", "vpnegressroute", "private", "-n", namespace, "--subresource=status", "--type=merge", "--dry-run=server", "-p", wrongKindConditionPatch)

	invalidRoute := fmt.Sprintf(`apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: invalid
  namespace: %s
spec:
  parentRefs: []
`, namespace)
	assertApplyFails(t, "API server accepted a route without a parent", nil, invalidRoute)

	alphaPod := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: alpha-annotation
  namespace: %s
  annotations:
    networking.waycloak.io/gateway: old
spec:
  containers:
    - name: app
      image: registry.k8s.io/pause:3.10.1
`, namespace)
	waitForApplyFailure(t, alphaPod)
	assertApplyFails(t, "API server accepted an alpha Waycloak annotation", nil, alphaPod)
	invalidEnrollment := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: invalid-enrollment
  namespace: %s
  labels:
    networking.waycloak.io/egress-route: route.with.dot
spec:
  containers:
    - name: app
      image: registry.k8s.io/pause:3.10.1
`, namespace)
	assertApplyFails(t, "API server accepted a non-DNS-label route lookup key", nil, invalidEnrollment)
	pods := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: unprotected
  namespace: %s
spec:
  containers:
    - name: app
      image: registry.k8s.io/pause:3.10.1
---
apiVersion: v1
kind: Pod
metadata:
  name: protected
  namespace: %s
  labels:
    networking.waycloak.io/egress-route: private
spec:
  automountServiceAccountToken: false
  containers:
    - name: app
      image: registry.k8s.io/pause:3.10.1
---
apiVersion: v1
kind: Pod
metadata:
  name: route-before-controller
  namespace: %s
  labels:
    networking.waycloak.io/egress-route: may-arrive-later
spec:
  containers:
    - name: app
      image: registry.k8s.io/pause:3.10.1
`, namespace, namespace, namespace)
	applyInput(t, nil, pods)
	assertApplicationPodUnmodified(t, namespace, "protected", "private")
	assertCommandFails(t, "live Pod enrollment label was mutable", nil, "kubectl", "label", "pod", "protected", "-n", namespace, "networking.waycloak.io/egress-route=other", "--overwrite")
	assertCommandFails(t, "unlabeled live Pod could be enrolled in place", nil, "kubectl", "label", "pod", "unprotected", "-n", namespace, "networking.waycloak.io/egress-route=private")
	verifyRouteControllerAndEnrollment(t, namespace)
	verifyUIDBoundBindings(t, namespace, release)
	verifyCrossNamespaceConsentWatches(t, namespace, className, suffix)

	binding := fmt.Sprintf(`apiVersion: networking.waycloak.io/v1beta1
kind: VPNWorkloadBinding
metadata:
  name: protected-11111111
  namespace: %s
spec:
  podRef:
    name: protected
    uid: 11111111-1111-1111-1111-111111111111
  routeRef:
    name: private
    uid: 22222222-2222-2222-2222-222222222222
  gatewayRef:
    namespace: %s
    name: private
    uid: 33333333-3333-3333-3333-333333333333
  nodeName: worker-1
  allocation:
    identity: allocation-1
    address: 100.64.0.2/32
  network:
    gatewayGeneration: 1
    overlayCIDR: 198.51.100.0/29
    gatewayAddress: 198.51.100.1
    gatewayEndpoint: 203.0.113.10:4789
    gatewayHealthPort: 18080
    vni: 7999
    mtu: 1320
    clusterTraffic:
      mode: TunnelAll
`, namespace, namespace)
	assertApplyFails(t, "ordinary user created a controller-authored binding", nil, binding)
	controllerUser := "system:serviceaccount:" + namespace + ":" + release
	applyInput(t, []string{"--as=" + controllerUser}, binding)
	command(t, nil, "kubectl", "get", "vpnworkloadbinding", "protected-11111111", "-n", namespace)
	command(t, nil, "kubectl", "delete", "vpnworkloadbinding", "protected-11111111", "-n", namespace, "--as="+controllerUser, "--wait=true", "--timeout=30s")
}

func verifyGatewayClassContract(t *testing.T, namespace, className string) {
	t.Helper()
	ctx := context.Background()
	config, err := ctrl.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{wayv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	client, err := ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	class := &wayv1.VPNGatewayClass{}
	if err := client.Get(ctx, ctrlclient.ObjectKey{Name: className}, class); err != nil {
		t.Fatal(err)
	}
	classReconciler := &waycontroller.VPNGatewayClassReconciler{
		Client: client, ControllerName: class.Spec.ControllerName, ReleaseIdentity: class.Spec.ReleaseIdentity,
		ConformanceProfile: class.Spec.ConformanceProfile, SupportedFeatures: wayv1.CoreFeatures(),
	}
	if _, err := classReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(class)}); err != nil {
		t.Fatal(err)
	}
	gatewayReconciler := &waycontroller.ReplacementVPNGatewayReconciler{
		Client: client, APIReader: client, ControllerName: class.Spec.ControllerName,
		ReleaseIdentity: class.Spec.ReleaseIdentity, ConformanceProfile: class.Spec.ConformanceProfile, SupportedFeatures: wayv1.CoreFeatures(),
		NativeConfigRoles: []wayv1.QualifiedName{waycontroller.GluetunEnvironmentRole}, CredentialRoles: []wayv1.QualifiedName{waycontroller.OpenVPNCredentialsRole},
	}
	request := ctrl.Request{NamespacedName: ctrlclient.ObjectKey{Namespace: namespace, Name: "private"}}
	if _, err := gatewayReconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	gateway := &wayv1.VPNGateway{}
	if err := client.Get(ctx, request.NamespacedName, gateway); err != nil {
		t.Fatal(err)
	}
	for conditionType, want := range map[string]metav1.ConditionStatus{wayv1.ConditionAccepted: metav1.ConditionTrue, wayv1.ConditionResolvedRefs: metav1.ConditionTrue, wayv1.ConditionProgrammed: metav1.ConditionFalse} {
		condition := apiMeta.FindStatusCondition(gateway.Status.Conditions, conditionType)
		if condition == nil || condition.Status != want || condition.ObservedGeneration != gateway.Generation {
			t.Fatalf("minimal gateway condition %s = %#v", conditionType, gateway.Status)
		}
	}
	if gateway.Status.GatewayClass == nil || gateway.Status.GatewayClass.ReleaseIdentity != class.Spec.ReleaseIdentity || len(gateway.Status.Addresses) != 0 {
		t.Fatalf("minimal gateway published invalid class or programming state: %#v", gateway.Status)
	}

	unsupported := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "unsupported", Namespace: namespace}, Spec: wayv1.VPNGatewaySpec{
		GatewayClassName: wayv1.ObjectName(className), RequestedFeatures: []wayv1.FeatureName{wayv1.FeaturePortForwardSingleActive},
		ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
	}}
	if err := client.Create(ctx, unsupported); err != nil {
		t.Fatal(err)
	}
	if _, err := gatewayReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(unsupported)}); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(ctx, ctrlclient.ObjectKeyFromObject(unsupported), unsupported); err != nil {
		t.Fatal(err)
	}
	condition := apiMeta.FindStatusCondition(unsupported.Status.Conditions, wayv1.ConditionAccepted)
	programmed := apiMeta.FindStatusCondition(unsupported.Status.Conditions, wayv1.ConditionProgrammed)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != wayv1.ReasonUnsupportedFeature || programmed == nil || programmed.Status != metav1.ConditionFalse || len(unsupported.Status.Addresses) != 0 {
		t.Fatalf("unsupported gateway was partially accepted: %#v", unsupported.Status)
	}

	missing := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "missing-class", Namespace: namespace}, Spec: wayv1.VPNGatewaySpec{
		GatewayClassName: "missing.waycloak.io", ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
	}}
	if err := client.Create(ctx, missing); err != nil {
		t.Fatal(err)
	}
	if _, err := gatewayReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(missing)}); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(ctx, ctrlclient.ObjectKeyFromObject(missing), missing); err != nil {
		t.Fatal(err)
	}
	resolved := apiMeta.FindStatusCondition(missing.Status.Conditions, wayv1.ConditionResolvedRefs)
	programmed = apiMeta.FindStatusCondition(missing.Status.Conditions, wayv1.ConditionProgrammed)
	if resolved == nil || resolved.Status != metav1.ConditionFalse || resolved.Reason != wayv1.ReasonRefNotFound || programmed == nil || programmed.Status != metav1.ConditionFalse || len(missing.Status.Addresses) != 0 {
		t.Fatalf("missing class was partially accepted: %#v", missing.Status)
	}

	foreignClass := &wayv1.VPNGatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "foreign.waycloak.io"}, Spec: class.Spec}
	foreignClass.Spec.ControllerName = "foreign.waycloak.io/controller"
	if err := client.Create(ctx, foreignClass); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Delete(context.Background(), foreignClass) })
	foreign := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "foreign-class", Namespace: namespace}, Spec: wayv1.VPNGatewaySpec{
		GatewayClassName: wayv1.ObjectName(foreignClass.Name), ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
	}}
	if err := client.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := gatewayReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(foreign)}); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(ctx, ctrlclient.ObjectKeyFromObject(foreign), foreign); err != nil {
		t.Fatal(err)
	}
	accepted := apiMeta.FindStatusCondition(foreign.Status.Conditions, wayv1.ConditionAccepted)
	programmed = apiMeta.FindStatusCondition(foreign.Status.Conditions, wayv1.ConditionProgrammed)
	if accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != wayv1.ReasonControllerNotFound || programmed == nil || programmed.Status != metav1.ConditionFalse || len(foreign.Status.Addresses) != 0 {
		t.Fatalf("foreign class was partially accepted: %#v", foreign.Status)
	}
}

func verifyUIDBoundBindings(t *testing.T, namespace, serviceAccount string) {
	t.Helper()
	ctx := context.Background()
	config, err := ctrl.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{wayv1.AddToScheme, corev1.AddToScheme, coordinationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	controllerConfig := rest.CopyConfig(config)
	controllerConfig.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + namespace + ":" + serviceAccount,
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"},
	}
	controllerClient, err := ctrlclient.New(controllerConfig, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	protected := waitForScheduledPod(t, ctx, admin, ctrlclient.ObjectKey{Namespace: namespace, Name: "protected"})
	peer := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "binding-peer", Namespace: namespace, Labels: map[string]string{enrollment.RouteLabel: "private"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}}
	if err := admin.Create(ctx, peer); err != nil {
		t.Fatal(err)
	}
	peer = waitForScheduledPod(t, ctx, admin, ctrlclient.ObjectKeyFromObject(peer))

	reconciler := &waycontroller.PodBindingReconciler{Client: controllerClient, APIReader: controllerClient}
	errors := make(chan error, 2)
	for _, pod := range []*corev1.Pod{protected, peer} {
		go func(pod *corev1.Pod) {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(pod)})
			errors <- err
		}(pod)
	}
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}

	bindings := make([]*wayv1.VPNWorkloadBinding, 0, 2)
	addresses := map[string]struct{}{}
	for _, pod := range []*corev1.Pod{protected, peer} {
		binding := &wayv1.VPNWorkloadBinding{}
		if err := controllerClient.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: waybinding.BindingName(pod.UID)}, binding); err != nil {
			t.Fatal(err)
		}
		if binding.Spec.PodRef.UID != wayv1.ObjectUID(pod.UID) || binding.Spec.NodeName != wayv1.ObjectName(pod.Spec.NodeName) {
			t.Fatalf("inexact binding = %#v", binding.Spec)
		}
		if _, found := addresses[binding.Spec.Allocation.Address]; found {
			t.Fatalf("concurrent binding collision at %s", binding.Spec.Allocation.Address)
		}
		addresses[binding.Spec.Allocation.Address] = struct{}{}
		bindings = append(bindings, binding)
	}
	leases := &coordinationv1.LeaseList{}
	if err := controllerClient.List(ctx, leases, ctrlclient.InNamespace(namespace), ctrlclient.MatchingLabels{waybinding.ReservationManagedByLabel: waybinding.ReservationManagedByValue}); err != nil {
		t.Fatal(err)
	}
	if len(leases.Items) != 2 {
		t.Fatalf("address reservations = %d, want 2", len(leases.Items))
	}

	lifecycle := &waycontroller.VPNWorkloadBindingReconciler{Client: controllerClient, CleanupTimeout: time.Millisecond, Now: func() time.Time { return time.Now().Add(time.Second) }}
	for _, binding := range bindings {
		if _, err := lifecycle.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(binding)}); err != nil {
			t.Fatal(err)
		}
		if err := controllerClient.Get(ctx, ctrlclient.ObjectKeyFromObject(binding), binding); err != nil {
			t.Fatal(err)
		}
		ready := apiMeta.FindStatusCondition(binding.Status.Conditions, wayv1.ConditionReady)
		if ready == nil || ready.Status != metav1.ConditionUnknown {
			t.Fatalf("desired-only binding claimed readiness: %#v", binding.Status)
		}
	}
	versions := []string{bindings[0].ResourceVersion, bindings[1].ResourceVersion}
	restarted := &waycontroller.PodBindingReconciler{Client: controllerClient, APIReader: controllerClient}
	for i, pod := range []*corev1.Pod{protected, peer} {
		if _, err := restarted.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(pod)}); err != nil {
			t.Fatal(err)
		}
		if err := controllerClient.Get(ctx, ctrlclient.ObjectKeyFromObject(bindings[i]), bindings[i]); err != nil {
			t.Fatal(err)
		}
		if bindings[i].ResourceVersion != versions[i] {
			t.Fatal("restart recovery rewrote an unchanged binding")
		}
	}

	oldUID := peer.UID
	oldBinding := bindings[1].DeepCopy()
	if err := admin.Delete(ctx, peer); err != nil {
		t.Fatal(err)
	}
	waitForBindingDeletionTimestamp(t, ctx, controllerClient, oldBinding)
	for range 2 {
		if _, err := lifecycle.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(oldBinding)}); err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
	}
	waitForObjectDeletion(t, ctx, controllerClient, ctrlclient.ObjectKeyFromObject(oldBinding), &wayv1.VPNWorkloadBinding{})
	waitForObjectDeletion(t, ctx, admin, ctrlclient.ObjectKeyFromObject(peer), &corev1.Pod{})
	replacement := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: peer.Name, Namespace: namespace, Labels: map[string]string{enrollment.RouteLabel: "private"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}}
	if err := admin.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	replacement = waitForScheduledPod(t, ctx, admin, ctrlclient.ObjectKeyFromObject(replacement))
	if replacement.UID == oldUID {
		t.Fatalf("Pod name reuse retained UID %q", oldUID)
	}
	if _, err := restarted.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(replacement)}); err != nil {
		t.Fatal(err)
	}
	if waybinding.BindingName(replacement.UID) == oldBinding.Name {
		t.Fatal("Pod name reuse retained binding identity")
	}
}

func waitForScheduledPod(t *testing.T, ctx context.Context, client ctrlclient.Client, key ctrlclient.ObjectKey) *corev1.Pod {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pod := &corev1.Pod{}
		if err := client.Get(ctx, key, pod); err != nil {
			t.Fatal(err)
		}
		if pod.Spec.NodeName != "" {
			return pod
		}
		if time.Now().After(deadline) {
			t.Fatalf("Pod %s was not scheduled", key)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitForBindingDeletionTimestamp(t *testing.T, ctx context.Context, client ctrlclient.Client, binding *wayv1.VPNWorkloadBinding) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := client.Get(ctx, ctrlclient.ObjectKeyFromObject(binding), binding); err != nil {
			t.Fatal(err)
		}
		if !binding.DeletionTimestamp.IsZero() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("binding %s did not begin deletion", binding.Name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func verifyRouteControllerAndEnrollment(t *testing.T, namespace string) {
	t.Helper()
	ctx := context.Background()
	config, err := ctrl.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client, err := ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &waycontroller.VPNEgressRouteReconciler{Client: client, Scheme: scheme, Now: func() time.Time { return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC) }}
	request := ctrl.Request{NamespacedName: ctrlclient.ObjectKey{Namespace: namespace, Name: "private"}}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	route := &wayv1.VPNEgressRoute{}
	if err := client.Get(ctx, request.NamespacedName, route); err != nil {
		t.Fatal(err)
	}
	for _, conditionType := range []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady} {
		condition := apiMeta.FindStatusCondition(route.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != route.Generation {
			t.Fatalf("route %s = %#v", conditionType, condition)
		}
	}
	pod := &corev1.Pod{}
	if err := client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: "protected"}, pod); err != nil {
		t.Fatal(err)
	}
	resolution, err := (enrollment.Resolver{Reader: client}).Resolve(ctx, namespace, pod.Name, pod.UID)
	if err != nil || !resolution.Enrolled || !resolution.Ready || resolution.RouteUID != route.UID {
		t.Fatalf("protected Pod resolution = %#v, error = %v", resolution, err)
	}
	missing := &corev1.Pod{}
	if err := client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: "route-before-controller"}, missing); err != nil {
		t.Fatal(err)
	}
	resolution, err = (enrollment.Resolver{Reader: client}).Resolve(ctx, namespace, missing.Name, missing.UID)
	if err != nil || !resolution.Enrolled || resolution.Ready || resolution.Reason != enrollment.ReasonRouteNotFound {
		t.Fatalf("GitOps ordering resolution = %#v, error = %v", resolution, err)
	}

	oldRouteUID := route.UID
	if err := client.Delete(ctx, route); err != nil {
		t.Fatal(err)
	}
	resolution, err = (enrollment.Resolver{Reader: client}).Resolve(ctx, namespace, pod.Name, pod.UID)
	if err != nil || !resolution.Enrolled || resolution.Ready || resolution.Reason != enrollment.ReasonRouteNotFound {
		t.Fatalf("deleted route resolution = %#v, error = %v", resolution, err)
	}
	replacement := &wayv1.VPNEgressRoute{ObjectMeta: metav1.ObjectMeta{Name: route.Name, Namespace: route.Namespace}, Spec: route.Spec}
	if err := client.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.UID == oldRouteUID {
		t.Fatalf("route name reuse retained UID %q", oldRouteUID)
	}
	resolution, err = (enrollment.Resolver{Reader: client}).Resolve(ctx, namespace, pod.Name, pod.UID)
	if err != nil || !resolution.Enrolled || resolution.Ready || resolution.RouteUID != replacement.UID {
		t.Fatalf("unprogrammed replacement route resolution = %#v, error = %v", resolution, err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	resolution, err = (enrollment.Resolver{Reader: client}).Resolve(ctx, namespace, pod.Name, pod.UID)
	if err != nil || !resolution.Enrolled || !resolution.Ready || resolution.RouteUID != replacement.UID {
		t.Fatalf("reprogrammed replacement route resolution = %#v, error = %v", resolution, err)
	}
}

func verifyCrossNamespaceConsentWatches(t *testing.T, sourceNamespace, className, suffix string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config, err := ctrl.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	manager, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &waycontroller.VPNEgressRouteReconciler{Client: manager.GetClient(), Scheme: scheme, APIReader: manager.GetAPIReader()}
	if err := reconciler.SetupWithManager(manager); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	if !manager.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("controller cache did not synchronize")
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("stop route manager: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("route manager did not stop")
		}
	}()

	apiClient, err := ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	source := &corev1.Namespace{}
	if err := apiClient.Get(ctx, ctrlclient.ObjectKey{Name: sourceNamespace}, source); err != nil {
		t.Fatal(err)
	}
	if source.Labels == nil {
		source.Labels = map[string]string{}
	}
	source.Labels["networking.waycloak.io/gateway-access"] = "allowed"
	if err := apiClient.Update(ctx, source); err != nil {
		t.Fatal(err)
	}

	targetNamespace := "waycloak-gateway-" + suffix
	if err := apiClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNamespace}}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = apiClient.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNamespace}})
	}()
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"networking.waycloak.io/gateway-access": "allowed"}}
	gateway := crossNamespaceGateway(targetNamespace, className, selector)
	if err := apiClient.Create(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	setGatewayReady(t, ctx, apiClient, gateway)
	route := &wayv1.VPNEgressRoute{ObjectMeta: metav1.ObjectMeta{Name: "cross-private", Namespace: sourceNamespace}, Spec: wayv1.VPNEgressRouteSpec{ParentRefs: []wayv1.GatewayParentReference{{Group: wayv1.GroupName, Kind: "VPNGateway", Namespace: wayv1.NamespaceName(targetNamespace), Name: wayv1.ObjectName(gateway.Name)}}}}
	if err := apiClient.Create(ctx, route); err != nil {
		t.Fatal(err)
	}
	waitForRouteCondition(t, ctx, apiClient, route, wayv1.ConditionReady, metav1.ConditionTrue, "Ready")
	if len(route.OwnerReferences) != 0 {
		t.Fatalf("cross-namespace route owner references = %#v", route.OwnerReferences)
	}

	if err := apiClient.Get(ctx, ctrlclient.ObjectKey{Name: sourceNamespace}, source); err != nil {
		t.Fatal(err)
	}
	delete(source.Labels, "networking.waycloak.io/gateway-access")
	if err := apiClient.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	waitForRouteCondition(t, ctx, apiClient, route, wayv1.ConditionResolvedRefs, metav1.ConditionFalse, "RefNotPermitted")
	denied := *apiMeta.FindStatusCondition(route.Status.Conditions, wayv1.ConditionResolvedRefs)
	waitForRouteCondition(t, ctx, apiClient, route, wayv1.ConditionReady, metav1.ConditionFalse, "NotReady")

	if err := apiClient.Delete(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	waitForObjectDeletion(t, ctx, apiClient, ctrlclient.ObjectKeyFromObject(gateway), &wayv1.VPNGateway{})
	if err := apiClient.Get(ctx, ctrlclient.ObjectKey{Name: sourceNamespace}, source); err != nil {
		t.Fatal(err)
	}
	source.Labels["networking.waycloak.io/gateway-access"] = "allowed"
	if err := apiClient.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	waitForRouteCondition(t, ctx, apiClient, route, wayv1.ConditionResolvedRefs, metav1.ConditionFalse, "RefNotPermitted")
	missing := apiMeta.FindStatusCondition(route.Status.Conditions, wayv1.ConditionResolvedRefs)
	if missing == nil || missing.Reason != denied.Reason || missing.Message != denied.Message {
		t.Fatalf("cross-namespace missing status leaked existence: denied=%#v missing=%#v", denied, missing)
	}

	gateway = crossNamespaceGateway(targetNamespace, className, selector)
	if err := apiClient.Create(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	setGatewayReady(t, ctx, apiClient, gateway)
	waitForRouteCondition(t, ctx, apiClient, route, wayv1.ConditionReady, metav1.ConditionTrue, "Ready")

	if err := apiClient.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway); err != nil {
		t.Fatal(err)
	}
	gateway.Spec.AllowedRoutes.Namespaces = wayv1.RouteNamespaces{From: wayv1.RouteNamespaceSame}
	if err := apiClient.Update(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	waitForRouteCondition(t, ctx, apiClient, route, wayv1.ConditionResolvedRefs, metav1.ConditionFalse, "RefNotPermitted")

	if err := apiClient.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway); err != nil {
		t.Fatal(err)
	}
	gateway.Spec.AllowedRoutes.Namespaces = wayv1.RouteNamespaces{From: wayv1.RouteNamespaceAll}
	if err := apiClient.Update(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	setGatewayReady(t, ctx, apiClient, gateway)
	waitForRouteCondition(t, ctx, apiClient, route, wayv1.ConditionReady, metav1.ConditionTrue, "Ready")
	if err := apiClient.Delete(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	waitForRouteCondition(t, ctx, apiClient, route, wayv1.ConditionResolvedRefs, metav1.ConditionFalse, "RefNotPermitted")
}

func crossNamespaceGateway(namespace, className string, selector *metav1.LabelSelector) *wayv1.VPNGateway {
	return &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: namespace}, Spec: wayv1.VPNGatewaySpec{GatewayClassName: wayv1.ObjectName(className), AllowedRoutes: wayv1.AllowedRoutes{Namespaces: wayv1.RouteNamespaces{From: wayv1.RouteNamespaceSelector, Selector: selector}}, ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll}}}
}

func setGatewayReady(t *testing.T, ctx context.Context, client ctrlclient.Client, gateway *wayv1.VPNGateway) {
	t.Helper()
	if err := client.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway); err != nil {
		t.Fatal(err)
	}
	gateway.Status.ObservedGeneration = gateway.Generation
	gateway.Status.SupportedFeatures = wayv1.CoreFeatures()
	gateway.Status.Conditions = nil
	for _, conditionType := range []string{wayv1.ConditionAccepted, wayv1.ConditionProgrammed, wayv1.ConditionReady} {
		gateway.Status.Conditions = append(gateway.Status.Conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: conditionType, Message: "Gateway observation is current", ObservedGeneration: gateway.Generation, LastTransitionTime: metav1.Now()})
	}
	if err := client.Status().Update(ctx, gateway); err != nil {
		t.Fatal(err)
	}
}

func waitForRouteCondition(t *testing.T, ctx context.Context, client ctrlclient.Client, route *wayv1.VPNEgressRoute, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := client.Get(ctx, ctrlclient.ObjectKeyFromObject(route), route); err != nil {
			t.Fatal(err)
		}
		condition := apiMeta.FindStatusCondition(route.Status.Conditions, conditionType)
		if condition != nil && condition.Status == status && condition.Reason == reason && condition.ObservedGeneration == route.Generation {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("route %s did not reach %s/%s: %#v", conditionType, status, reason, route.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitForObjectDeletion(t *testing.T, ctx context.Context, client ctrlclient.Client, key ctrlclient.ObjectKey, object ctrlclient.Object) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := client.Get(ctx, key, object)
		if apierrors.IsNotFound(err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("object %s was not deleted", key)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitForApplyFailure(t *testing.T, manifest string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		cmd := exec.Command("kubectl", "apply", "--server-side", "--dry-run=server", "--field-manager=waycloak-e2e-policy-probe", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		if _, err := cmd.CombinedOutput(); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("admission policy did not become active")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func applyInput(t *testing.T, prefixArgs []string, manifest string) {
	t.Helper()
	args := append(append([]string{}, prefixArgs...), "apply", "--server-side", "--field-manager=waycloak-e2e", "-f", "-")
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = strings.NewReader(manifest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s", args, err, output)
	}
}

func assertApplyFails(t *testing.T, failureMessage string, prefixArgs []string, manifest string) {
	t.Helper()
	args := append(append([]string{}, prefixArgs...), "apply", "--server-side", "--field-manager=waycloak-e2e", "-f", "-")
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = strings.NewReader(manifest)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("%s: %s", failureMessage, output)
	}
}

func assertCommandFails(t *testing.T, failureMessage string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if env != nil {
		cmd.Env = env
	}
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("%s: %s", failureMessage, output)
	}
}

func assertApplicationPodUnmodified(t *testing.T, namespace, name, route string) {
	t.Helper()
	var pod corev1.Pod
	if err := json.Unmarshal([]byte(command(t, nil, "kubectl", "get", "pod", name, "-n", namespace, "-o", "json")), &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Labels["networking.waycloak.io/egress-route"] != route {
		t.Fatalf("enrollment label = %q, want %q", pod.Labels["networking.waycloak.io/egress-route"], route)
	}
	for key := range pod.Annotations {
		if strings.HasPrefix(key, "networking.waycloak.io/") || strings.HasPrefix(key, "internal.networking.waycloak.io/") {
			t.Fatalf("application Pod received Waycloak annotation %q", key)
		}
	}
	if len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 || len(pod.Spec.Containers) != 1 {
		t.Fatalf("application Pod was injected: init=%d ephemeral=%d containers=%d", len(pod.Spec.InitContainers), len(pod.Spec.EphemeralContainers), len(pod.Spec.Containers))
	}
	container := pod.Spec.Containers[0]
	if container.Name != "app" || container.Image != "registry.k8s.io/pause:3.10.1" ||
		len(container.Command) != 0 || len(container.Args) != 0 ||
		len(container.Ports) != 0 || len(container.EnvFrom) != 0 || len(container.Env) != 0 ||
		len(container.VolumeMounts) != 0 || len(container.VolumeDevices) != 0 ||
		container.WorkingDir != "" || container.LivenessProbe != nil || container.ReadinessProbe != nil ||
		container.StartupProbe != nil || container.Lifecycle != nil || container.SecurityContext != nil ||
		len(container.Resources.Limits) != 0 || len(container.Resources.Requests) != 0 || len(container.Resources.Claims) != 0 {
		t.Fatalf("application container received Waycloak wiring: %#v", container)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken ||
		len(pod.Spec.Volumes) != 0 || hasInjectedPodSecurityContext(pod.Spec.SecurityContext) ||
		pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC {
		t.Fatalf("application Pod received credential, volume, capability, or host wiring: %#v", pod.Spec)
	}
}

func hasInjectedPodSecurityContext(securityContext *corev1.PodSecurityContext) bool {
	if securityContext == nil {
		return false
	}
	defaultSupplementalGroupsPolicy := securityContext.SupplementalGroupsPolicy == nil ||
		*securityContext.SupplementalGroupsPolicy == corev1.SupplementalGroupsPolicyMerge
	return securityContext.SELinuxOptions != nil || securityContext.WindowsOptions != nil ||
		securityContext.RunAsUser != nil || securityContext.RunAsGroup != nil ||
		securityContext.RunAsNonRoot != nil || len(securityContext.SupplementalGroups) != 0 ||
		!defaultSupplementalGroupsPolicy || securityContext.FSGroup != nil ||
		len(securityContext.Sysctls) != 0 || securityContext.FSGroupChangePolicy != nil ||
		securityContext.SeccompProfile != nil || securityContext.AppArmorProfile != nil ||
		securityContext.SELinuxChangePolicy != nil
}
