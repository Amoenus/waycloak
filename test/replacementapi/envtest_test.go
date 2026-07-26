// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build envtest

package replacementapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
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

		installBindingPolicy(t, ctx, admin, namespace.Name, "controller")
		controller := mustClient(t, impersonate(config, namespace.Name, "controller"), scheme)
		outsider := mustClient(t, impersonate(config, namespace.Name, "outsider"), scheme)
		waitForBindingAdmissionDeny(t, ctx, outsider, namespace.Name)
		allowed := validBindingForNamespace("controller-binding", namespace.Name)
		must(t, controller.Create(ctx, allowed))
		mustRejectForbidden(t, outsider.Create(ctx, validBindingForNamespace("outsider-binding", namespace.Name)))

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
	})

	t.Run("deletion does not cascade user intent", func(t *testing.T) {
		class := validClass("deletion")
		gateway := validGateway("deletion")
		must(t, admin.Create(ctx, class))
		must(t, admin.Create(ctx, gateway))
		must(t, admin.Delete(ctx, class))
		must(t, admin.Get(ctx, ctrlclient.ObjectKeyFromObject(gateway), &wayv1.VPNGateway{}))
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
	return &wayv1.VPNGateway{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNGateway"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
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
	for _, name := range []string{"controller-role.yaml", "distribution-role.yaml", "network-operator-role.yaml", "workload-owner-role.yaml", "adapter-operator-role.yaml", "node-agent-role.yaml"} {
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
			Validations: []admissionv1.Validation{{Expression: fmt.Sprintf("request.userInfo.username in [%q, %q]", username, "system:serviceaccount:kube-system:generic-garbage-collector"), Message: "VPNWorkloadBinding is controller-authored and cannot be changed by users", Reason: &reason}},
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
