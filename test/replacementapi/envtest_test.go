// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build envtest

package replacementapi

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"github.com/Amoenus/waycloak/internal/enrollment"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

const testNamespace = "replacement-api-test"

func TestReplacementAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repositoryRoot, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: os.Getenv("KUBEBUILDER_ASSETS"),
	}
	environment.ControlPlane.GetAPIServer().Configure().Set("authorization-mode", "RBAC")
	environment.ControlPlane.GetAPIServer().Configure().Append("enable-admission-plugins", "ValidatingAdmissionPolicy")
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil && !strings.Contains(err.Error(), "not supported by windows") {
			t.Errorf("stop envtest: %v", err)
		}
	})

	scheme := runtime.NewScheme()
	must(t, wayv1.AddToScheme(scheme))
	must(t, corev1.AddToScheme(scheme))
	must(t, rbacv1.AddToScheme(scheme))
	must(t, admissionv1.AddToScheme(scheme))
	must(t, coordinationv1.AddToScheme(scheme))
	admin := mustClient(t, config, scheme)
	must(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}))

	t.Run("fresh discovery serves only replacement kinds", func(t *testing.T) {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
		must(t, err)
		resources, err := discoveryClient.ServerResourcesForGroupVersion(wayv1.GroupVersion.String())
		must(t, err)
		var primary []string
		for _, resource := range resources.APIResources {
			if !strings.Contains(resource.Name, "/") {
				primary = append(primary, resource.Name)
			}
		}
		sort.Strings(primary)
		want := []string{"portforwardleases", "vpnegressroutes", "vpngatewayclasses", "vpngateways", "vpnworkloadbindings", "workloadadapters"}
		if fmt.Sprint(primary) != fmt.Sprint(want) {
			t.Fatalf("discovered resources = %v, want %v", primary, want)
		}
		if _, err := discoveryClient.ServerResourcesForGroupVersion("networking.waycloak.io/v1alpha1"); !apierrors.IsNotFound(err) {
			t.Fatalf("alpha discovery error = %v, want NotFound", err)
		}
	})

	t.Run("strict writes reject unknown fields", func(t *testing.T) {
		dynamicClient, err := dynamic.NewForConfig(config)
		must(t, err)
		resource := dynamicClient.Resource(schema.GroupVersionResource{Group: wayv1.GroupName, Version: "v1beta1", Resource: "vpngateways"}).Namespace(testNamespace)
		object := validGateway("strict-unknown")
		data, err := json.Marshal(object)
		must(t, err)
		var value map[string]any
		must(t, json.Unmarshal(data, &value))
		value["spec"].(map[string]any)["ordinaryEgressFallback"] = true
		_, err = resource.Create(ctx, &unstructured.Unstructured{Object: value}, metav1.CreateOptions{FieldValidation: metav1.FieldValidationStrict})
		if err == nil || !strings.Contains(err.Error(), "ordinaryEgressFallback") {
			t.Fatalf("strict create error = %v, want unknown-field rejection", err)
		}
	})

	t.Run("defaults and structural validation", func(t *testing.T) {
		class := validClass("defaulting")
		must(t, admin.Create(ctx, class))
		gateway := validGateway("defaulting")
		must(t, admin.Create(ctx, gateway))
		storedGateway := &wayv1.VPNGateway{}
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), storedGateway))
		if storedGateway.Spec.AllowedRoutes.Namespaces.From != wayv1.RouteNamespaceSame || storedGateway.Spec.DNS.Mode != wayv1.DNSModeGateway {
			t.Fatalf("gateway defaults = allowedRoutes:%q dns:%q", storedGateway.Spec.AllowedRoutes.Namespaces.From, storedGateway.Spec.DNS.Mode)
		}

		lease := validLease("defaulting")
		must(t, admin.Create(ctx, lease))
		storedLease := &wayv1.PortForwardLease{}
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(lease), storedLease))
		if storedLease.Spec.EndpointPolicy != wayv1.EndpointPolicySingleActive {
			t.Fatalf("endpointPolicy = %q, want SingleActive", storedLease.Spec.EndpointPolicy)
		}

		invalidClass := validClass("missing-core")
		invalidClass.Spec.SupportedFeatures = invalidClass.Spec.SupportedFeatures[:5]
		mustReject(t, admin.Create(ctx, invalidClass), "frozen Core feature")
		secretParameters := validClass("secret-parameters")
		secretParameters.Spec.ParametersRef = &wayv1.ClusterObjectReference{Group: "core.example.io", Kind: "Secret", Name: "credentials"}
		mustReject(t, admin.Create(ctx, secretParameters), "non-Secret")

		emptyParents := validRoute("empty-parents")
		emptyParents.Spec.ParentRefs = nil
		mustReject(t, admin.Create(ctx, emptyParents), "parentRefs")
		twoParents := validRoute("two-parents")
		twoParents.Spec.ParentRefs = append(twoParents.Spec.ParentRefs, wayv1.GatewayParentReference{Group: wayv1.GroupName, Kind: "VPNGateway", Namespace: testNamespace, Name: "other"})
		mustReject(t, admin.Create(ctx, twoParents), "parentRefs")
		wrongParent := validRoute("wrong-parent")
		wrongParent.Spec.ParentRefs[0].Group = "other.example.io"
		mustReject(t, admin.Create(ctx, wrongParent), "parentRefs")

		wrongBackend := validLease("wrong-backend")
		wrongBackend.Spec.BackendRef.Group = "apps"
		mustReject(t, admin.Create(ctx, wrongBackend), "core Service")
	})

	t.Run("immutable identities and condition reasons are enforced", func(t *testing.T) {
		class := validClass("immutable")
		must(t, admin.Create(ctx, class))
		class.Spec.ReleaseIdentity.Version = "changed"
		mustReject(t, admin.Update(ctx, class), "immutable")

		gateway := validGateway("immutable")
		must(t, admin.Create(ctx, gateway))
		gateway.Spec.GatewayClassName = "other.example.io"
		mustReject(t, admin.Update(ctx, gateway), "immutable")

		adapter := validAdapter("immutable")
		must(t, admin.Create(ctx, adapter))
		adapter.Spec.ProtocolVersion = "networking.waycloak.io/adapter/changed"
		mustReject(t, admin.Update(ctx, adapter), "immutable")

		binding := validBinding("immutable")
		must(t, admin.Create(ctx, binding))
		binding.Spec.PodRef.UID = "changed"
		mustReject(t, admin.Update(ctx, binding), "immutable")

		route := validRoute("immutable-status")
		must(t, admin.Create(ctx, route))
		route.Status.Parents = []wayv1.RouteParentStatus{{ParentRef: route.Spec.ParentRefs[0], ControllerName: "example.waycloak.io/controller"}}
		must(t, admin.Status().Update(ctx, route))
		route.Status.Parents[0].ControllerName = "other.example.io/controller"
		mustReject(t, admin.Status().Update(ctx, route), "controllerName")

		stored := &wayv1.VPNEgressRoute{}
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(route), stored))
		stored.Status.Parents = nil
		mustReject(t, admin.Status().Update(ctx, stored), "cannot be cleared")

		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(route), stored))
		stored.Status.Conditions = wayv1.Conditions{
			{Type: wayv1.ConditionAccepted, Status: metav1.ConditionUnknown, Reason: "ObservationUnavailable", LastTransitionTime: metav1.Now()},
			{Type: wayv1.ConditionResolvedRefs, Status: metav1.ConditionUnknown, Reason: "ObservationUnavailable", LastTransitionTime: metav1.Now()},
		}
		must(t, admin.Status().Update(ctx, stored))

		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(route), stored))
		stored.Status.Conditions = wayv1.Conditions{{Type: wayv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Registered", LastTransitionTime: metav1.Now()}}
		mustReject(t, admin.Status().Update(ctx, stored), "Ready condition reason")
	})

	t.Run("status subresource and server-side ownership", func(t *testing.T) {
		route := validRoute("status-ownership")
		route.Status.Conditions = wayv1.Conditions{{Type: wayv1.ConditionAccepted, Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()}}
		must(t, admin.Create(ctx, route))
		stored := &wayv1.VPNEgressRoute{}
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(route), stored))
		if len(stored.Status.Conditions) != 0 {
			t.Fatalf("status was persisted through the main resource endpoint")
		}

		dynamicClient, err := dynamic.NewForConfig(config)
		must(t, err)
		resource := dynamicClient.Resource(schema.GroupVersionResource{Group: wayv1.GroupName, Version: "v1beta1", Resource: "vpnegressroutes"}).Namespace(testNamespace)
		applyStatus(t, ctx, resource, route.Name, "waycloak-summary-a", map[string]any{"conditions": []any{condition("Accepted", "Accepted")}})
		applyStatus(t, ctx, resource, route.Name, "waycloak-summary-b", map[string]any{"conditions": []any{condition("Ready", "Ready")}})
		result, err := resource.Get(ctx, route.Name, metav1.GetOptions{})
		must(t, err)
		conditions, found, err := unstructured.NestedSlice(result.Object, "status", "conditions")
		must(t, err)
		if !found || len(conditions) != 2 {
			t.Fatalf("merged conditions = %#v, want two manager-owned entries", conditions)
		}

		parent := map[string]any{
			"parentRef":      map[string]any{"group": wayv1.GroupName, "kind": "VPNGateway", "namespace": testNamespace, "name": "gateway"},
			"controllerName": "example.waycloak.io/controller",
			"conditions":     []any{condition("Accepted", "Accepted")},
		}
		applyStatus(t, ctx, resource, route.Name, "waycloak-route-a", map[string]any{"parents": []any{parent}})
		parent["conditions"] = []any{condition("Ready", "Ready")}
		err = applyStatusError(ctx, resource, route.Name, "waycloak-route-b", map[string]any{"parents": []any{parent}})
		if !apierrors.IsConflict(err) {
			t.Fatalf("atomic parent ownership error = %v, want Conflict", err)
		}
	})

	t.Run("resource condition vocabularies are stable and scoped", func(t *testing.T) {
		gateway := validGateway("component-conditions")
		must(t, admin.Create(ctx, gateway))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway))
		gateway.Status.ObservedGeneration = gateway.Generation
		gateway.Status.Conditions = wayv1.GatewayConditions{
			currentCondition(wayv1.ConditionTunnelReady, metav1.ConditionTrue, wayv1.ReasonTunnelReady, gateway.Generation),
			currentCondition(wayv1.ConditionDNSReady, metav1.ConditionUnknown, wayv1.ReasonObservationUnavailable, gateway.Generation),
			currentCondition(wayv1.ConditionMembershipApplied, metav1.ConditionFalse, wayv1.ReasonMembershipPending, gateway.Generation),
		}
		must(t, admin.Status().Update(ctx, gateway))

		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway))
		gateway.Status.Conditions[0].Reason = wayv1.ReasonReady
		mustReject(t, admin.Status().Update(ctx, gateway), "TunnelReady condition reason")

		route := validRoute("component-scope")
		must(t, admin.Create(ctx, route))
		route.Status.Conditions = wayv1.Conditions{
			currentCondition(wayv1.ConditionTunnelReady, metav1.ConditionTrue, wayv1.ReasonTunnelReady, route.Generation),
		}
		mustReject(t, admin.Status().Update(ctx, route), "condition type")

		binding := validBinding("component-conditions")
		must(t, admin.Create(ctx, binding))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(binding), binding))
		binding.Status.ObservedGeneration = binding.Generation
		binding.Status.Conditions = wayv1.BindingConditions{
			currentCondition(wayv1.ConditionNodeReady, metav1.ConditionTrue, wayv1.ReasonNodeReady, binding.Generation),
		}
		must(t, admin.Status().Update(ctx, binding))

		lease := validLease("component-conditions")
		must(t, admin.Create(ctx, lease))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(lease), lease))
		lease.Status.ObservedGeneration = lease.Generation
		lease.Status.Conditions = wayv1.LeaseConditions{
			currentCondition(wayv1.ConditionGatewayRulesReady, metav1.ConditionTrue, wayv1.ReasonGatewayRulesReady, lease.Generation),
			currentCondition(wayv1.ConditionDelivered, metav1.ConditionTrue, wayv1.ReasonDelivered, lease.Generation),
			currentCondition(wayv1.ConditionAcknowledged, metav1.ConditionFalse, wayv1.ReasonAcknowledgementPending, lease.Generation),
		}
		must(t, admin.Status().Update(ctx, lease))
	})

	t.Run("route reconciliation and exact Pod enrollment", func(t *testing.T) {
		gateway := validGateway("route-parent")
		must(t, admin.Create(ctx, gateway))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway))
		gateway.Status.ObservedGeneration = gateway.Generation
		gateway.Status.SupportedFeatures = wayv1.CoreFeatures()
		gateway.Status.Conditions = currentTrueConditions(gateway.Generation, wayv1.ConditionAccepted, wayv1.ConditionProgrammed, wayv1.ConditionReady)
		must(t, admin.Status().Update(ctx, gateway))

		route := validRoute("route-reconciled")
		route.Spec.ParentRefs[0].Name = wayv1.ObjectName(gateway.Name)
		must(t, admin.Create(ctx, route))
		reconciler := &waycontroller.VPNEgressRouteReconciler{Client: admin, Scheme: scheme, Now: func() time.Time { return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC) }}
		request := ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(route)}
		_, err := reconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, route))
		assertCurrentTrueConditions(t, route)
		assertManagedBy(t, route, wayv1.FieldManagerRoutePrefix+"core")
		firstVersion := route.ResourceVersion
		_, err = reconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, route))
		if route.ResourceVersion != firstVersion {
			t.Fatalf("no-op reconciliation wrote status: resourceVersion %s -> %s", firstVersion, route.ResourceVersion)
		}

		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "route-enrolled", Namespace: testNamespace, Labels: map[string]string{enrollment.RouteLabel: route.Name}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}}
		must(t, admin.Create(ctx, pod))
		resolved, err := (enrollment.Resolver{Reader: admin}).Resolve(ctx, pod.Namespace, pod.Name, pod.UID)
		must(t, err)
		if !resolved.Enrolled || !resolved.Ready || resolved.RouteUID != route.UID {
			t.Fatalf("ready exact-UID resolution = %#v", resolved)
		}
		if _, err := (enrollment.Resolver{Reader: admin}).Resolve(ctx, pod.Namespace, pod.Name, types.UID("reused-name-uid")); !stderrors.Is(err, enrollment.ErrPodUIDMismatch) {
			t.Fatalf("name-reuse resolution error = %v, want ErrPodUIDMismatch", err)
		}

		route.Spec.ParentRefs[0].Name = "missing-parent"
		must(t, admin.Update(ctx, route))
		_, err = reconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, route))
		if condition := apiMeta.FindStatusCondition(route.Status.Conditions, wayv1.ConditionResolvedRefs); condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "RefNotFound" {
			t.Fatalf("changed parent status = %#v", route.Status)
		}
		resolved, err = (enrollment.Resolver{Reader: admin}).Resolve(ctx, pod.Namespace, pod.Name, pod.UID)
		must(t, err)
		if !resolved.Enrolled || resolved.Ready {
			t.Fatalf("changed route must retain fail-closed enrollment: %#v", resolved)
		}

		must(t, admin.Delete(ctx, route))
		resolved, err = (enrollment.Resolver{Reader: admin}).Resolve(ctx, pod.Namespace, pod.Name, pod.UID)
		must(t, err)
		if !resolved.Enrolled || resolved.Ready || resolved.Reason != enrollment.ReasonRouteNotFound {
			t.Fatalf("deleted route must retain fail-closed enrollment: %#v", resolved)
		}
	})

	t.Run("concurrent route reconciliation converges without timestamp churn", func(t *testing.T) {
		gateway := validGateway("route-concurrent-parent")
		must(t, admin.Create(ctx, gateway))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway))
		gateway.Status.ObservedGeneration = gateway.Generation
		gateway.Status.SupportedFeatures = wayv1.CoreFeatures()
		gateway.Status.Conditions = currentTrueConditions(gateway.Generation, wayv1.ConditionAccepted, wayv1.ConditionProgrammed, wayv1.ConditionReady)
		must(t, admin.Status().Update(ctx, gateway))

		route := validRoute("route-concurrent")
		route.Spec.ParentRefs[0].Name = wayv1.ObjectName(gateway.Name)
		must(t, admin.Create(ctx, route))
		now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
		reconciler := &waycontroller.VPNEgressRouteReconciler{Client: admin, Scheme: scheme, Now: func() time.Time { return now }}
		request := ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(route)}
		errors := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := reconciler.Reconcile(ctx, request)
				errors <- err
			}()
		}
		for range 2 {
			must(t, <-errors)
		}
		must(t, admin.Get(ctx, request.NamespacedName, route))
		assertCurrentTrueConditions(t, route)
		firstVersion := route.ResourceVersion
		firstTransitions := make(map[string]metav1.Time, len(route.Status.Conditions))
		for _, condition := range route.Status.Conditions {
			firstTransitions[condition.Type] = condition.LastTransitionTime
		}
		for range 2 {
			_, err := reconciler.Reconcile(ctx, request)
			must(t, err)
		}
		must(t, admin.Get(ctx, request.NamespacedName, route))
		if route.ResourceVersion != firstVersion {
			t.Fatalf("converged reconciliation wrote status: resourceVersion %s -> %s", firstVersion, route.ResourceVersion)
		}
		for _, condition := range route.Status.Conditions {
			firstTransition := firstTransitions[condition.Type]
			if !condition.LastTransitionTime.Equal(&firstTransition) {
				t.Fatalf("%s transition churned: %s -> %s", condition.Type, firstTransitions[condition.Type], condition.LastTransitionTime)
			}
		}
	})

	t.Run("UID-bound allocation survives concurrency restart and stale observation", func(t *testing.T) {
		now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
		gateway := validGateway("binding-parent")
		must(t, admin.Create(ctx, gateway))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway))
		gateway.Status.ObservedGeneration = gateway.Generation
		gateway.Status.SupportedFeatures = wayv1.CoreFeatures()
		gateway.Status.Addresses = []wayv1.GatewayAddress{
			{Type: wayv1.GatewayAddressTypeOverlayCIDR, Value: "198.51.100.0/29"},
			{Type: wayv1.GatewayAddressTypeOverlayAddress, Value: "198.51.100.1"},
			{Type: wayv1.GatewayAddressTypeUnderlayEndpoint, Value: "192.0.2.10:4789"},
			{Type: wayv1.GatewayAddressTypeOverlayHealthPort, Value: "18080"},
			{Type: wayv1.GatewayAddressTypeVNI, Value: "7999"},
			{Type: wayv1.GatewayAddressTypeMTU, Value: "1320"},
		}
		gateway.Status.Conditions = currentTrueConditions(gateway.Generation, wayv1.ConditionAccepted, wayv1.ConditionProgrammed, wayv1.ConditionReady)
		must(t, admin.Status().Update(ctx, gateway))

		route := validRoute("binding-route")
		route.Spec.ParentRefs[0].Name = wayv1.ObjectName(gateway.Name)
		must(t, admin.Create(ctx, route))
		routeReconciler := &waycontroller.VPNEgressRouteReconciler{Client: admin, Scheme: scheme, Now: func() time.Time { return now }}
		_, err := routeReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(route)})
		must(t, err)

		pods := []*corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Name: "binding-pod-a", Namespace: testNamespace, Labels: map[string]string{enrollment.RouteLabel: route.Name}}, Spec: corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "binding-pod-b", Namespace: testNamespace, Labels: map[string]string{enrollment.RouteLabel: route.Name}}, Spec: corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}},
		}
		for _, pod := range pods {
			must(t, admin.Create(ctx, pod))
		}
		allocator := &waycontroller.PodBindingReconciler{Client: admin, APIReader: admin, Now: func() time.Time { return now }}
		errors := make(chan error, len(pods))
		for _, pod := range pods {
			go func(pod *corev1.Pod) {
				_, err := allocator.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(pod)})
				errors <- err
			}(pod)
		}
		for range pods {
			must(t, <-errors)
		}

		bindings := make([]*wayv1.VPNWorkloadBinding, 0, len(pods))
		addresses := map[string]struct{}{}
		for _, pod := range pods {
			binding := &wayv1.VPNWorkloadBinding{}
			must(t, admin.Get(ctx, ctrlclient.ObjectKey{Namespace: pod.Namespace, Name: waybinding.BindingName(pod.UID)}, binding))
			if binding.Spec.PodRef.UID != wayv1.ObjectUID(pod.UID) || binding.Spec.GatewayRef.UID != wayv1.ObjectUID(gateway.UID) {
				t.Fatalf("inexact binding = %#v", binding.Spec)
			}
			assertMainManagedBy(t, binding, wayv1.FieldManagerBindingController)
			if _, exists := addresses[binding.Spec.Allocation.Address]; exists {
				t.Fatalf("concurrent address collision at %s", binding.Spec.Allocation.Address)
			}
			addresses[binding.Spec.Allocation.Address] = struct{}{}
			bindings = append(bindings, binding)
		}
		leases := &coordinationv1.LeaseList{}
		must(t, admin.List(ctx, leases, ctrlclient.InNamespace(gateway.Namespace), ctrlclient.MatchingLabels{waybinding.ReservationManagedByLabel: waybinding.ReservationManagedByValue}))
		if len(leases.Items) != len(pods) {
			t.Fatalf("reservations = %d, want %d", len(leases.Items), len(pods))
		}

		// A new reconciler instance recovers the exact Lease transaction without
		// changing either allocation or resource version.
		restarted := &waycontroller.PodBindingReconciler{Client: admin, APIReader: admin, Now: func() time.Time { return now.Add(time.Second) }}
		for i, pod := range pods {
			version := bindings[i].ResourceVersion
			_, err := restarted.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(pod)})
			must(t, err)
			current := &wayv1.VPNWorkloadBinding{}
			must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(bindings[i]), current))
			if current.ResourceVersion != version || current.Spec.Allocation != bindings[i].Spec.Allocation {
				t.Fatalf("restart changed persisted allocation: %#v", current)
			}
		}

		binding := bindings[0]
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(binding), binding))
		binding.Status.AppliedGeneration = binding.Generation
		binding.Status.ObservedPodUID = "stale-pod-uid"
		binding.Status.ObservedGatewayUID = binding.Spec.GatewayRef.UID
		binding.Status.Agent = &wayv1.NodeAgentObservation{NodeName: binding.Spec.NodeName, NodeBootID: "boot-a", InstanceID: "agent-a", ObservedAt: metav1.NewTime(now)}
		must(t, admin.Status().Update(ctx, binding))
		lifecycle := &waycontroller.VPNWorkloadBindingReconciler{Client: admin, Now: func() time.Time { return now }}
		_, err = lifecycle.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(binding)})
		must(t, err)
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(binding), binding))
		if condition := apiMeta.FindStatusCondition(binding.Status.Conditions, wayv1.ConditionReady); condition == nil || condition.Status != metav1.ConditionFalse {
			t.Fatalf("stale identity readiness = %#v", binding.Status)
		}
		assertManagedBy(t, binding, wayv1.FieldManagerBindingController)

		// An applied allocation cannot be silently reused when withdrawal is
		// unconfirmed. The bounded finalizer recreates a missing reservation as
		// a durable quarantine before allowing deletion.
		quarantined := bindings[1]
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(quarantined), quarantined))
		quarantined.Status.AppliedGeneration = quarantined.Generation
		must(t, admin.Status().Update(ctx, quarantined))
		reservation, err := waybinding.ReservationForBinding(quarantined)
		must(t, err)
		must(t, admin.Delete(ctx, reservation))
		must(t, admin.Delete(ctx, quarantined))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(quarantined), quarantined))
		if quarantined.DeletionTimestamp.IsZero() {
			t.Fatal("binding deletion timestamp was not persisted")
		}
		cleanupNow := quarantined.DeletionTimestamp.Add(11 * time.Minute)
		cleanup := &waycontroller.VPNWorkloadBindingReconciler{Client: admin, Now: func() time.Time { return cleanupNow }}
		storedReservation := &coordinationv1.Lease{}
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err = cleanup.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(quarantined)})
			if err != nil && !stderrors.Is(err, waybinding.ErrReservationDeleting) && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
			getErr := admin.Get(ctx, ctrlclient.ObjectKeyFromObject(reservation), storedReservation)
			if getErr == nil && storedReservation.Annotations[waybinding.ReservationStateAnnotation] == waybinding.ReservationStateQuarantined {
				break
			}
			if getErr != nil && !apierrors.IsNotFound(getErr) {
				t.Fatal(getErr)
			}
			if time.Now().After(deadline) {
				remaining := &coordinationv1.LeaseList{}
				listErr := admin.List(ctx, remaining, ctrlclient.InNamespace(reservation.Namespace))
				currentGateway := &wayv1.VPNGateway{}
				gatewayErr := admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), currentGateway)
				currentBinding := &wayv1.VPNWorkloadBinding{}
				bindingErr := admin.Get(ctx, ctrlclient.ObjectKeyFromObject(quarantined), currentBinding)
				t.Fatalf("durable quarantine did not converge: reconcile=%v get=%v leases=%d list=%v gateway=%v/%s binding=%v/%v", err, getErr, len(remaining.Items), listErr, gatewayErr, currentGateway.UID, bindingErr, currentBinding.Finalizers)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if storedReservation.Annotations[waybinding.ReservationStateAnnotation] != waybinding.ReservationStateQuarantined {
			t.Fatalf("reservation state = %q, want quarantine", storedReservation.Annotations[waybinding.ReservationStateAnnotation])
		}
	})

	t.Run("cross namespace consent is private and fail closed", func(t *testing.T) {
		targetNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "replacement-cross-gateways"}}
		must(t, admin.Create(ctx, targetNamespace))
		sourceNamespace := &corev1.Namespace{}
		must(t, admin.Get(ctx, ctrlclient.ObjectKey{Name: testNamespace}, sourceNamespace))
		sourceNamespace.Labels = map[string]string{"networking.waycloak.io/gateway-access": "allowed"}
		must(t, admin.Update(ctx, sourceNamespace))
		selector := &metav1.LabelSelector{MatchLabels: map[string]string{"networking.waycloak.io/gateway-access": "allowed"}}
		gateway := validGatewayForNamespace("cross-private", targetNamespace.Name)
		gateway.Spec.AllowedRoutes.Namespaces = wayv1.RouteNamespaces{From: wayv1.RouteNamespaceSelector, Selector: selector}
		must(t, admin.Create(ctx, gateway))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), gateway))
		gateway.Status.ObservedGeneration = gateway.Generation
		gateway.Status.SupportedFeatures = wayv1.CoreFeatures()
		gateway.Status.Conditions = currentTrueConditions(gateway.Generation, wayv1.ConditionAccepted, wayv1.ConditionProgrammed, wayv1.ConditionReady)
		must(t, admin.Status().Update(ctx, gateway))
		route := validRoute("cross-private")
		route.Spec.ParentRefs[0].Namespace = wayv1.NamespaceName(targetNamespace.Name)
		route.Spec.ParentRefs[0].Name = wayv1.ObjectName(gateway.Name)
		must(t, admin.Create(ctx, route))
		reconciler := &waycontroller.VPNEgressRouteReconciler{Client: admin, APIReader: admin, Scheme: scheme}
		request := ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(route)}
		_, err := reconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, route))
		assertCurrentTrueConditions(t, route)
		if len(route.OwnerReferences) != 0 {
			t.Fatalf("cross-namespace owner references = %#v", route.OwnerReferences)
		}

		must(t, admin.Get(ctx, ctrlclient.ObjectKey{Name: testNamespace}, sourceNamespace))
		delete(sourceNamespace.Labels, "networking.waycloak.io/gateway-access")
		must(t, admin.Update(ctx, sourceNamespace))
		_, err = reconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, route))
		denied := apiMeta.FindStatusCondition(route.Status.Conditions, wayv1.ConditionResolvedRefs)
		if denied == nil || denied.Status != metav1.ConditionFalse || denied.Reason != "RefNotPermitted" {
			t.Fatalf("revoked consent = %#v", route.Status)
		}
		deniedMessage := denied.Message

		must(t, admin.Delete(ctx, gateway))
		must(t, admin.Get(ctx, ctrlclient.ObjectKey{Name: testNamespace}, sourceNamespace))
		sourceNamespace.Labels["networking.waycloak.io/gateway-access"] = "allowed"
		must(t, admin.Update(ctx, sourceNamespace))
		_, err = reconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, route))
		missing := apiMeta.FindStatusCondition(route.Status.Conditions, wayv1.ConditionResolvedRefs)
		if missing == nil || missing.Status != metav1.ConditionFalse || missing.Reason != "RefNotPermitted" || missing.Message != deniedMessage {
			t.Fatalf("missing target leaked existence: denied=%q missing=%#v", deniedMessage, missing)
		}
	})

	t.Run("Pod enrollment admission rejects alpha and live mutation", func(t *testing.T) {
		installPodEnrollmentPolicy(t, ctx, admin)
		waitForPodEnrollmentPolicy(t, ctx, admin)
		alpha := testPod("alpha-annotation", map[string]string{"networking.waycloak.io/gateway": "old"}, nil)
		mustRejectForbidden(t, admin.Create(ctx, alpha))
		invalid := testPod("invalid-enrollment", nil, map[string]string{enrollment.RouteLabel: "route.with.dot"})
		mustReject(t, admin.Create(ctx, invalid), "same-namespace DNS label")
		unlabeled := testPod("unlabeled", nil, nil)
		must(t, admin.Create(ctx, unlabeled))
		valid := testPod("valid-enrollment", nil, map[string]string{enrollment.RouteLabel: "route-may-arrive-later"})
		must(t, admin.Create(ctx, valid))
		valid.Labels[enrollment.RouteLabel] = "other-route"
		mustRejectForbidden(t, admin.Update(ctx, valid))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(unlabeled), unlabeled))
		unlabeled.Labels = map[string]string{enrollment.RouteLabel: "late-enrollment"}
		mustRejectForbidden(t, admin.Update(ctx, unlabeled))
	})

	t.Run("persona RBAC and binding admission deny users", func(t *testing.T) {
		installRoles(t, ctx, admin, repositoryRoot)
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "replacement-rbac"}}
		must(t, admin.Create(ctx, namespace))
		for _, name := range []string{"owner", "controller", "outsider", "node-agent"} {
			must(t, admin.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace.Name}}))
		}
		bindRole(t, ctx, admin, namespace.Name, "owner", "waycloak-workload-owner")
		bindRole(t, ctx, admin, namespace.Name, "controller", "waycloak-controller")
		bindRole(t, ctx, admin, namespace.Name, "outsider", "waycloak-controller")
		bindRole(t, ctx, admin, namespace.Name, "node-agent", "waycloak-node-agent")

		owner := mustClient(t, impersonate(config, namespace.Name, "owner"), scheme)
		ownerRoute := validRouteForNamespace("owner", namespace.Name)
		must(t, owner.Create(ctx, ownerRoute))
		mustRejectForbidden(t, owner.Create(ctx, validBindingForNamespace("owner-binding", namespace.Name)))
		namespace.Labels = map[string]string{"networking.waycloak.io/gateway-access": "allowed"}
		mustRejectForbidden(t, owner.Update(ctx, namespace))

		installBindingPolicy(t, ctx, admin, namespace.Name, "controller")
		controller := mustClient(t, impersonate(config, namespace.Name, "controller"), scheme)
		outsider := mustClient(t, impersonate(config, namespace.Name, "outsider"), scheme)
		waitForBindingAdmissionDeny(t, ctx, outsider, namespace.Name)
		allowed := validBindingForNamespace("controller-binding", namespace.Name)
		must(t, controller.Create(ctx, allowed))
		mustRejectForbidden(t, outsider.Create(ctx, validBindingForNamespace("outsider-binding", namespace.Name)))
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: namespace.Name}}
		must(t, admin.Create(ctx, secret))
		mustRejectForbidden(t, controller.Get(ctx, ctrlclient.ObjectKeyFromObject(secret), &corev1.Secret{}))
		bindRole(t, ctx, admin, namespace.Name, "controller", "waycloak-gateway-secret-reader")
		must(t, controller.Get(ctx, ctrlclient.ObjectKeyFromObject(secret), &corev1.Secret{}))
		mustRejectForbidden(t, controller.List(ctx, &corev1.SecretList{}, ctrlclient.InNamespace(namespace.Name)))

		nodeAgent := mustClient(t, impersonate(config, namespace.Name, "node-agent"), scheme)
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: namespace.Name}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}}
		must(t, admin.Create(ctx, pod))
		must(t, nodeAgent.Get(ctx, ctrlclient.ObjectKeyFromObject(pod), &corev1.Pod{}))
		must(t, nodeAgent.Get(ctx, ctrlclient.ObjectKeyFromObject(allowed), &wayv1.VPNWorkloadBinding{}))
		must(t, nodeAgent.List(ctx, &corev1.PodList{}, ctrlclient.InNamespace(namespace.Name)))
		must(t, nodeAgent.List(ctx, &wayv1.VPNWorkloadBindingList{}, ctrlclient.InNamespace(namespace.Name)))
		mustRejectForbidden(t, nodeAgent.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace.Name, Name: "credentials"}, &corev1.Secret{}))
		mustRejectForbidden(t, nodeAgent.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace.Name, Name: "gateway"}, &wayv1.VPNGateway{}))
		statusAttempt := allowed.DeepCopy()
		statusAttempt.Status.ObservedGeneration = statusAttempt.Generation
		mustRejectForbidden(t, nodeAgent.Status().Update(ctx, statusAttempt))
		allowed.Spec.Allocation.Address = "192.0.2.11/32"
		mustRejectForbidden(t, outsider.Update(ctx, allowed))
		mustRejectForbidden(t, outsider.Delete(ctx, allowed))

		cleanup := validBindingForNamespace("namespace-cleanup", namespace.Name)
		must(t, controller.Create(ctx, cleanup))
		must(t, admin.Delete(ctx, namespace))
		must(t, outsider.Delete(ctx, cleanup))
	})

	t.Run("deletion does not cascade user intent", func(t *testing.T) {
		class := validClass("deletion")
		gateway := validGateway("deletion")
		must(t, admin.Create(ctx, class))
		must(t, admin.Create(ctx, gateway))
		must(t, admin.Delete(ctx, class))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), &wayv1.VPNGateway{}))
	})

	t.Run("class and gateway controllers reject unsupported intent before programming", func(t *testing.T) {
		class := validClass("controller-contract")
		class.Spec.ControllerName = waycontroller.DefaultGatewayControllerName
		must(t, admin.Create(ctx, class))
		classReconciler := &waycontroller.VPNGatewayClassReconciler{
			Client: admin, ControllerName: class.Spec.ControllerName, ReleaseIdentity: class.Spec.ReleaseIdentity,
			ConformanceProfile: class.Spec.ConformanceProfile, SupportedFeatures: wayv1.CoreFeatures(),
		}
		_, err := classReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(class)})
		must(t, err)
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(class), class))
		for _, conditionType := range []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady} {
			condition := apiMeta.FindStatusCondition(class.Status.Conditions, conditionType)
			if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != class.Generation || class.Status.ObservedGeneration != class.Generation {
				t.Fatalf("class condition %s = %#v", conditionType, class.Status)
			}
		}
		assertManagedBy(t, class, wayv1.FieldManagerClassController)
		classResourceVersion := class.ResourceVersion
		_, err = classReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(class)})
		must(t, err)
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(class), class))
		if class.ResourceVersion != classResourceVersion {
			t.Fatalf("no-op class reconciliation wrote status: %s -> %s", classResourceVersion, class.ResourceVersion)
		}

		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "controller-native", Namespace: testNamespace}}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "controller-credentials", Namespace: testNamespace}, Data: map[string][]byte{"password": []byte("must-not-enter-status")}}
		must(t, admin.Create(ctx, configMap))
		must(t, admin.Create(ctx, secret))
		gateway := validGateway("controller-contract")
		gateway.Spec.GatewayClassName = wayv1.ObjectName(class.Name)
		gateway.Spec.NativeConfigRefs = []wayv1.RoleObjectReference{{Role: waycontroller.GluetunEnvironmentRole, Name: wayv1.ObjectName(configMap.Name)}}
		gateway.Spec.CredentialRefs = []wayv1.RoleObjectReference{{Role: waycontroller.OpenVPNCredentialsRole, Name: wayv1.ObjectName(secret.Name)}}
		must(t, admin.Create(ctx, gateway))
		gatewayReconciler := &waycontroller.ReplacementVPNGatewayReconciler{
			Client: admin, APIReader: admin, ControllerName: class.Spec.ControllerName,
			ReleaseIdentity: class.Spec.ReleaseIdentity, ConformanceProfile: class.Spec.ConformanceProfile, SupportedFeatures: wayv1.CoreFeatures(),
			NativeConfigRoles: []wayv1.QualifiedName{waycontroller.GluetunEnvironmentRole}, CredentialRoles: []wayv1.QualifiedName{waycontroller.OpenVPNCredentialsRole},
		}
		request := ctrl.Request{NamespacedName: ctrlclient.ObjectKeyFromObject(gateway)}
		_, err = gatewayReconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, gateway))
		if condition := apiMeta.FindStatusCondition(gateway.Status.Conditions, wayv1.ConditionAccepted); condition == nil || condition.Status != metav1.ConditionTrue {
			t.Fatalf("accepted gateway status = %#v", gateway.Status)
		}
		if condition := apiMeta.FindStatusCondition(gateway.Status.Conditions, wayv1.ConditionResolvedRefs); condition == nil || condition.Status != metav1.ConditionTrue {
			t.Fatalf("resolved gateway status = %#v", gateway.Status)
		}
		if condition := apiMeta.FindStatusCondition(gateway.Status.Conditions, wayv1.ConditionProgrammed); condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != wayv1.ReasonPending {
			t.Fatalf("unimplemented programming status = %#v", gateway.Status)
		}
		if len(gateway.Status.Addresses) != 0 || strings.Contains(fmt.Sprint(gateway.Status), "must-not-enter-status") {
			t.Fatalf("gateway status exposed credentials or partial programming: %#v", gateway.Status)
		}
		assertManagedBy(t, gateway, wayv1.FieldManagerGatewayController)
		gatewayResourceVersion := gateway.ResourceVersion
		_, err = gatewayReconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, gateway))
		if gateway.ResourceVersion != gatewayResourceVersion {
			t.Fatalf("no-op gateway reconciliation wrote status: %s -> %s", gatewayResourceVersion, gateway.ResourceVersion)
		}

		gateway.Spec.RequestedFeatures = []wayv1.FeatureName{wayv1.FeaturePortForwardSingleActive}
		must(t, admin.Update(ctx, gateway))
		_, err = gatewayReconciler.Reconcile(ctx, request)
		must(t, err)
		must(t, admin.Get(ctx, request.NamespacedName, gateway))
		unsupported := apiMeta.FindStatusCondition(gateway.Status.Conditions, wayv1.ConditionAccepted)
		if unsupported == nil || unsupported.Status != metav1.ConditionFalse || unsupported.Reason != wayv1.ReasonUnsupportedFeature || len(gateway.Status.Addresses) != 0 {
			t.Fatalf("unsupported gateway status = %#v", gateway.Status)
		}
	})
}

func validClass(name string) *wayv1.VPNGatewayClass {
	return &wayv1.VPNGatewayClass{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNGatewayClass"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: wayv1.VPNGatewayClassSpec{
			ControllerName:     "example.waycloak.io/controller",
			ReleaseIdentity:    wayv1.ReleaseIdentity{Version: "v1.0.0-beta.1", ManifestDigest: "sha256:" + strings.Repeat("1", 64)},
			SupportedFeatures:  wayv1.CoreFeatures(),
			ConformanceProfile: "networking.waycloak.io/Core-v1",
		},
	}
}

func validGateway(name string) *wayv1.VPNGateway {
	return validGatewayForNamespace(name, testNamespace)
}

func validGatewayForNamespace(name, namespace string) *wayv1.VPNGateway {
	return &wayv1.VPNGateway{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNGateway"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       wayv1.VPNGatewaySpec{GatewayClassName: "example.waycloak.io", ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll}},
	}
}

func validRoute(name string) *wayv1.VPNEgressRoute {
	return validRouteForNamespace(name, testNamespace)
}

func validRouteForNamespace(name, namespace string) *wayv1.VPNEgressRoute {
	return &wayv1.VPNEgressRoute{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNEgressRoute"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       wayv1.VPNEgressRouteSpec{ParentRefs: []wayv1.GatewayParentReference{{Group: wayv1.GroupName, Kind: "VPNGateway", Namespace: wayv1.NamespaceName(namespace), Name: "gateway"}}},
	}
}

func validBinding(name string) *wayv1.VPNWorkloadBinding {
	return validBindingForNamespace(name, testNamespace)
}

func validBindingForNamespace(name, namespace string) *wayv1.VPNWorkloadBinding {
	return &wayv1.VPNWorkloadBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNWorkloadBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: wayv1.VPNWorkloadBindingSpec{
			PodRef:     wayv1.LocalUIDReference{Name: "pod", UID: wayv1.ObjectUID("pod-" + name)},
			RouteRef:   wayv1.LocalUIDReference{Name: "route", UID: wayv1.ObjectUID("route-" + name)},
			GatewayRef: wayv1.NamespacedUIDReference{Namespace: wayv1.NamespaceName(namespace), Name: "gateway", UID: wayv1.ObjectUID("gateway-" + name)},
			NodeName:   "node.example.io",
			Allocation: wayv1.WorkloadAllocation{Identity: "allocation-" + name, Address: "192.0.2.10/32"},
			Network: wayv1.WorkloadNetworkIntent{
				GatewayGeneration: 1, OverlayCIDR: "192.0.2.0/24", GatewayAddress: "192.0.2.1",
				GatewayEndpoint: "198.51.100.10:4789", GatewayHealthPort: 18080, VNI: 7999, MTU: 1320,
				ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
			},
		},
	}
}

func validLease(name string) *wayv1.PortForwardLease {
	return &wayv1.PortForwardLease{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "PortForwardLease"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: wayv1.PortForwardLeaseSpec{
			GatewayRef: wayv1.NamespacedObjectReference{Namespace: testNamespace, Name: "gateway"},
			BackendRef: wayv1.ServiceBackendReference{Group: "", Kind: "Service", Name: "backend", Port: intstr.FromString("peer")},
			Protocols:  []wayv1.TransportProtocol{wayv1.ProtocolTCP},
		},
	}
}

func validAdapter(name string) *wayv1.WorkloadAdapter {
	return &wayv1.WorkloadAdapter{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "WorkloadAdapter"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: wayv1.WorkloadAdapterSpec{
			Image:                 "registry.invalid/waycloak/adapter@sha256:" + strings.Repeat("2", 64),
			ProtocolVersion:       "networking.waycloak.io/adapter/v1",
			SupportedApplications: []wayv1.QualifiedName{"example.waycloak.io/application"},
		},
	}
}

func condition(conditionType, reason string) map[string]any {
	return map[string]any{"type": conditionType, "status": "True", "reason": reason, "message": "observed", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}
}

func currentTrueConditions(generation int64, conditionTypes ...string) wayv1.GatewayConditions {
	result := make(wayv1.GatewayConditions, 0, len(conditionTypes))
	for _, conditionType := range conditionTypes {
		result = append(result, metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: conditionType, ObservedGeneration: generation, LastTransitionTime: metav1.Now()})
	}
	return result
}

func currentCondition(conditionType string, status metav1.ConditionStatus, reason string, generation int64) metav1.Condition {
	return metav1.Condition{
		Type: conditionType, Status: status, Reason: reason, Message: "Observed state is non-sensitive",
		ObservedGeneration: generation, LastTransitionTime: metav1.Now(),
	}
}

func assertCurrentTrueConditions(t *testing.T, route *wayv1.VPNEgressRoute) {
	t.Helper()
	if route.Status.ObservedGeneration != route.Generation || len(route.Status.Parents) != 1 || route.Status.Parents[0].ControllerName != waycontroller.RouteControllerName {
		t.Fatalf("route status identity = %#v", route.Status)
	}
	for _, conditionType := range []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady} {
		condition := apiMeta.FindStatusCondition(route.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != route.Generation {
			t.Fatalf("current %s = %#v", conditionType, condition)
		}
	}
}

func assertManagedBy(t *testing.T, object metav1.Object, manager string) {
	t.Helper()
	for _, entry := range object.GetManagedFields() {
		if entry.Manager == manager && entry.Subresource == "status" {
			return
		}
	}
	t.Fatalf("status managedFields has no %q owner: %#v", manager, object.GetManagedFields())
}

func assertMainManagedBy(t *testing.T, object metav1.Object, manager string) {
	t.Helper()
	for _, entry := range object.GetManagedFields() {
		if entry.Manager == manager && entry.Subresource == "" {
			return
		}
	}
	t.Fatalf("managedFields has no main-resource %q owner: %#v", manager, object.GetManagedFields())
}

func testPod(name string, annotations, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Annotations: annotations, Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10.1"}}}}
}

func installPodEnrollmentPolicy(t *testing.T, ctx context.Context, client ctrlclient.Client) {
	t.Helper()
	failure := admissionv1.Fail
	exact := admissionv1.Exact
	forbidden := metav1.StatusReasonForbidden
	invalid := metav1.StatusReasonInvalid
	policy := &admissionv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "waycloak-pod-enrollment-test"},
		Spec: admissionv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: &failure,
			MatchConstraints: &admissionv1.MatchResources{
				MatchPolicy: &exact,
				ResourceRules: []admissionv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionv1.RuleWithOperations{
						Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update},
						Rule: admissionv1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"pods"},
							Scope:       scope(admissionv1.NamespacedScope),
						},
					},
				}},
			},
			Validations: []admissionv1.Validation{
				{Expression: `!has(object.metadata.annotations) || object.metadata.annotations.all(key, !key.startsWith("networking.waycloak.io/") && !key.startsWith("internal.networking.waycloak.io/"))`, Message: "alpha Waycloak annotations are not accepted by the replacement API", Reason: &forbidden},
				{Expression: `!has(object.metadata.labels) || !("networking.waycloak.io/egress-route" in object.metadata.labels) || object.metadata.labels["networking.waycloak.io/egress-route"].matches("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")`, Message: "the Waycloak egress-route label must contain one same-namespace DNS label", Reason: &invalid},
				{Expression: `request.operation != "UPDATE" || (has(object.metadata.labels) && "networking.waycloak.io/egress-route" in object.metadata.labels ? object.metadata.labels["networking.waycloak.io/egress-route"] : "") == (has(oldObject.metadata.labels) && "networking.waycloak.io/egress-route" in oldObject.metadata.labels ? oldObject.metadata.labels["networking.waycloak.io/egress-route"] : "")`, Message: "enrollment on an existing Pod is immutable; update the Pod template and create a new Pod", Reason: &forbidden},
			},
		},
	}
	must(t, client.Create(ctx, policy))
	must(t, client.Create(ctx, &admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: policy.Name}, Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: policy.Name, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny}}}))
}

func waitForPodEnrollmentPolicy(t *testing.T, ctx context.Context, client ctrlclient.Client) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := client.Create(ctx, testPod("policy-probe", map[string]string{"networking.waycloak.io/gateway": "old"}, nil), &ctrlclient.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if apierrors.IsForbidden(err) {
			return
		}
		if err != nil {
			t.Fatalf("Pod admission probe error = %v, want Forbidden", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("Pod enrollment admission policy did not become active")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func applyStatus(t *testing.T, ctx context.Context, resource dynamic.ResourceInterface, name, manager string, status map[string]any) {
	t.Helper()
	must(t, applyStatusError(ctx, resource, name, manager, status))
}

func applyStatusError(ctx context.Context, resource dynamic.ResourceInterface, name, manager string, status map[string]any) error {
	data, err := json.Marshal(map[string]any{"apiVersion": wayv1.GroupVersion.String(), "kind": "VPNEgressRoute", "metadata": map[string]any{"name": name}, "status": status})
	if err != nil {
		return err
	}
	_, err = resource.Patch(ctx, name, types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: manager}, "status")
	return err
}

func installRoles(t *testing.T, ctx context.Context, client ctrlclient.Client, root string) {
	t.Helper()
	for _, name := range []string{"controller-role.yaml", "distribution-role.yaml", "network-operator-role.yaml", "workload-owner-role.yaml", "adapter-operator-role.yaml", "node-agent-role.yaml", "gateway-secret-reader-role.yaml"} {
		data, err := os.ReadFile(filepath.Join(root, "config", "rbac", name))
		must(t, err)
		role := &rbacv1.ClusterRole{}
		must(t, yaml.Unmarshal(data, role))
		must(t, client.Create(ctx, role))
	}
}

func bindRole(t *testing.T, ctx context.Context, client ctrlclient.Client, namespace, serviceAccount, clusterRole string) {
	t.Helper()
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: serviceAccount + "-" + clusterRole, Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccount, Namespace: namespace}},
	}
	must(t, client.Create(ctx, binding))
}

func installBindingPolicy(t *testing.T, ctx context.Context, client ctrlclient.Client, namespace, controllerServiceAccount string) {
	t.Helper()
	failure := admissionv1.Fail
	exact := admissionv1.Exact
	reason := metav1.StatusReasonForbidden
	username := fmt.Sprintf("system:serviceaccount:%s:%s", namespace, controllerServiceAccount)
	policy := &admissionv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "waycloak-binding-guard-test"},
		Spec: admissionv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: &failure,
			MatchConstraints: &admissionv1.MatchResources{
				MatchPolicy: &exact,
				ResourceRules: []admissionv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionv1.RuleWithOperations{
						Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update, admissionv1.Delete},
						Rule:       admissionv1.Rule{APIGroups: []string{wayv1.GroupName}, APIVersions: []string{"v1beta1"}, Resources: []string{"vpnworkloadbindings", "vpnworkloadbindings/status"}, Scope: scope(admissionv1.NamespacedScope)},
					},
				}},
			},
			Validations: []admissionv1.Validation{{Expression: fmt.Sprintf("request.userInfo.username in [%q, %q] || (request.operation == 'DELETE' && namespaceObject != null && has(namespaceObject.metadata.deletionTimestamp))", username, "system:serviceaccount:kube-system:generic-garbage-collector"), Message: "VPNWorkloadBinding is controller-authored and cannot be changed by users", Reason: &reason}},
		},
	}
	must(t, client.Create(ctx, policy))
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: policy.Name}, Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: policy.Name, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny}}}
	must(t, client.Create(ctx, binding))
}

func waitForBindingAdmissionDeny(t *testing.T, ctx context.Context, client ctrlclient.Client, namespace string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := client.Create(ctx, validBindingForNamespace("admission-probe", namespace), &ctrlclient.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if apierrors.IsForbidden(err) {
			return
		}
		if err != nil {
			t.Fatalf("admission probe error = %v, want Forbidden", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("binding admission policy did not become active")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func scope(value admissionv1.ScopeType) *admissionv1.ScopeType { return &value }

func impersonate(config *rest.Config, namespace, serviceAccount string) *rest.Config {
	result := rest.CopyConfig(config)
	result.Impersonate = rest.ImpersonationConfig{UserName: fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccount), Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"}}
	return result
}

func mustClient(t *testing.T, config *rest.Config, scheme *runtime.Scheme) ctrlclient.Client {
	t.Helper()
	client, err := ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
	must(t, err)
	return client
}

func mustReject(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("error = %v, want rejection containing %q", err, contains)
	}
}

func mustRejectForbidden(t *testing.T, err error) {
	t.Helper()
	if !apierrors.IsForbidden(err) {
		t.Fatalf("error = %v, want Forbidden", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
