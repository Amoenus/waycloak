// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestStateBackupExportsOnlyPortableIntentDeterministically(t *testing.T) {
	clients := stateTestClients(t, "source")
	seedPortableState(t, clients)

	first, err := BuildStateBackup(context.Background(), clients)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildStateBackup(context.Background(), clients)
	if err != nil {
		t.Fatal(err)
	}
	if first.BackupID != second.BackupID || len(first.Resources) != 4 {
		t.Fatalf("portable backup is not deterministic or complete: %#v %#v", first, second)
	}
	for _, resource := range first.Resources {
		if resource.Kind == "VPNWorkloadBinding" {
			t.Fatal("controller-owned binding was exported")
		}
		if _, found := resource.Spec["status"]; found {
			t.Fatalf("status entered the portable spec for %#v", resource)
		}
	}
	encoded, err := json.Marshal(first.Resources)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"CANARY-SECRET", "binding-uid", "provider-public-address", "runtime-endpoint"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("portable state leaked %q: %s", forbidden, encoded)
		}
	}

	tampered := first
	tampered.Resources = append([]PortableStateResource(nil), first.Resources...)
	tampered.Resources[0].Spec = copyAnyMap(first.Resources[0].Spec)
	tampered.Resources[0].Spec["tampered"] = true
	if err := tampered.validate(); err == nil || !strings.Contains(err.Error(), "backupID") {
		t.Fatalf("tampered backup was accepted: %v", err)
	}
}

func TestStateRestoreIsTargetBoundConfirmationGatedAndIdempotent(t *testing.T) {
	source := stateTestClients(t, "source")
	seedPortableState(t, source)
	backup, err := BuildStateBackup(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}

	target := stateTestClients(t, "target")
	seedStateClass(t, target)
	plan, err := BuildStateRestorePlan(context.Background(), target, backup, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if reflectClusterIdentity(plan.Target, backup.Source) {
		t.Fatal("restore plan did not bind a distinct target cluster")
	}
	encodedPlan, _ := json.Marshal(plan)
	var tamperedPlan StateRestorePlan
	if err := json.Unmarshal(encodedPlan, &tamperedPlan); err != nil {
		t.Fatal(err)
	}
	tamperedPlan.Resources[0].Spec["tampered"] = true
	tamperedPlan.PlanID, err = tamperedPlan.identityDigest()
	if err != nil {
		t.Fatal(err)
	}
	if err := tamperedPlan.validate(); err == nil || !strings.Contains(err.Error(), "does not match its backup") {
		t.Fatalf("plan resource tampering retained an unrelated backup identity: %v", err)
	}
	if err := ApplyStateRestorePlan(context.Background(), target, plan, "wrong"); err == nil || !strings.Contains(err.Error(), "refusing mutation") {
		t.Fatalf("wrong confirmation was accepted: %v", err)
	}
	assertPortableStateCount(t, target, 0)

	if err := ApplyStateRestorePlan(context.Background(), target, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	assertPortableStateCount(t, target, 4)
	if err := ApplyStateRestorePlan(context.Background(), target, plan, plan.PlanID); err != nil {
		t.Fatalf("exact partial retry was not idempotent: %v", err)
	}
	assertPortableStateCount(t, target, 4)

	bindingType, _ := stateResourceFor("VPNGateway")
	gateway, err := target.Dynamic.Resource(bindingType.GVR).Namespace("media").Get(context.Background(), "private", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if gateway.GetAnnotations()[stateRestorePlanKey] != plan.PlanID {
		t.Fatalf("restored object lacks exact plan ownership: %#v", gateway.GetAnnotations())
	}
	if _, found, _ := unstructured.NestedMap(gateway.Object, "status"); found {
		t.Fatal("restore imported status")
	}
	bindingGVR := doctorResources[3].GVR
	bindings, err := target.Dynamic.Resource(bindingGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(bindings.Items) != 0 {
		t.Fatalf("restore imported a controller binding: %#v %v", bindings, err)
	}
}

func TestStateRestoreRefusesConflictsAndTargetDriftBeforeMutation(t *testing.T) {
	source := stateTestClients(t, "source")
	seedPortableState(t, source)
	backup, err := BuildStateBackup(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}

	conflicted := stateTestClients(t, "conflicted")
	seedStateClass(t, conflicted)
	gatewayType, _ := stateResourceFor("VPNGateway")
	foreign := stateObject("VPNGateway", "media", "private", map[string]any{"gatewayClassName": "gluetun.waycloak.io", "foreign": true})
	if _, err = conflicted.Dynamic.Resource(gatewayType.GVR).Namespace("media").Create(context.Background(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildStateRestorePlan(context.Background(), conflicted, backup, "100.96.0.0/16"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unowned conflict was accepted: %v", err)
	}

	target := stateTestClients(t, "target")
	seedStateClass(t, target)
	plan, err := BuildStateRestorePlan(context.Background(), target, backup, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	node, err := target.Kubernetes.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.NodeInfo.KernelVersion = "6.9.0"
	if _, err = target.Kubernetes.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = ApplyStateRestorePlan(context.Background(), target, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "observation changed") {
		t.Fatalf("target drift was accepted: %v", err)
	}
	assertPortableStateCount(t, target, 0)
}

func TestStateRestoreRefusesObjectCreatedInFinalRaceWindow(t *testing.T) {
	source := stateTestClients(t, "source")
	seedPortableState(t, source)
	backup, err := BuildStateBackup(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	target := stateTestClients(t, "target")
	seedStateClass(t, target)
	plan, err := BuildStateRestorePlan(context.Background(), target, backup, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}

	dynamicClient := target.Dynamic.(*dynamicfake.FakeDynamicClient)
	injected := false
	dynamicClient.PrependReactor("create", "workloadadapters", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if injected {
			return false, nil, nil
		}
		injected = true
		create := action.(clienttesting.CreateAction)
		foreign := stateObject("WorkloadAdapter", "media", "qbittorrent", map[string]any{"foreign": true})
		if err := dynamicClient.Tracker().Create(action.GetResource(), foreign, action.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource}, create.GetObject().(metav1.Object).GetName())
	})
	if err := ApplyStateRestorePlan(context.Background(), target, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "restore race") {
		t.Fatalf("final create race was adopted: %v", err)
	}
	adapterType, _ := stateResourceFor("WorkloadAdapter")
	foreign, err := target.Dynamic.Resource(adapterType.GVR).Namespace("media").Get(context.Background(), "qbittorrent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if foreign.GetAnnotations()[stateRestorePlanKey] != "" {
		t.Fatalf("foreign raced object was mutated: %#v", foreign.Object)
	}
	for _, resourceType := range portableStateResources[1:] {
		list, err := target.Dynamic.Resource(resourceType.GVR).List(context.Background(), metav1.ListOptions{})
		if err != nil || len(list.Items) != 0 {
			t.Fatalf("restore continued after raced conflict: %#v %v", list, err)
		}
	}
}

func TestStateRestoreRequiresExactCRDsClassesAndNamespaces(t *testing.T) {
	source := stateTestClients(t, "source")
	seedPortableState(t, source)
	backup, err := BuildStateBackup(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}

	wrongCRD := stateTestClients(t, "wrong-crd")
	seedStateClass(t, wrongCRD)
	crd, err := wrongCRD.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(context.Background(), stateCRDNames[0], metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	crd.Spec.Names.ShortNames = []string{"changed"}
	if _, err = wrongCRD.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Update(context.Background(), crd, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildStateRestorePlan(context.Background(), wrongCRD, backup, "100.96.0.0/16"); err == nil || !strings.Contains(err.Error(), "CRD identities") {
		t.Fatalf("changed CRD identity was accepted: %v", err)
	}

	wrongClass := stateTestClients(t, "wrong-class")
	seedStateClass(t, wrongClass)
	class, err := wrongClass.Dynamic.Resource(gatewayClassGVR).Get(context.Background(), "gluetun.waycloak.io", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(class.Object, "v1.0.0-beta.2", "spec", "releaseIdentity", "version"); err != nil {
		t.Fatal(err)
	}
	if _, err = wrongClass.Dynamic.Resource(gatewayClassGVR).Update(context.Background(), class, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildStateRestorePlan(context.Background(), wrongClass, backup, "100.96.0.0/16"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed class identity was accepted: %v", err)
	}

	missingNamespace := stateTestClients(t, "missing-namespace")
	seedStateClass(t, missingNamespace)
	if err = missingNamespace.Kubernetes.CoreV1().Namespaces().Delete(context.Background(), "media", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildStateRestorePlan(context.Background(), missingNamespace, backup, "100.96.0.0/16"); err == nil || !strings.Contains(err.Error(), "required target namespace") {
		t.Fatalf("missing namespace was accepted: %v", err)
	}
}

func stateTestClients(t *testing.T, identity string) *Clients {
	t.Helper()
	clients := supportedClients(t)
	clients.ClusterServerFingerprint = fingerprintText("server-" + identity)
	clients.ClusterTrustFingerprint = fingerprintText("trust-" + identity)
	system, err := clients.Kubernetes.CoreV1().Namespaces().Get(context.Background(), "kube-system", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	system.UID = k8stypes.UID("cluster-" + identity)
	if _, err = clients.Kubernetes.CoreV1().Namespaces().Update(context.Background(), system, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = clients.Kubernetes.CoreV1().Namespaces().Create(context.Background(), namespaceObject("media"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	crds := make([]runtime.Object, 0, len(stateCRDNames))
	for _, name := range stateCRDNames {
		plural := strings.TrimSuffix(name, ".networking.waycloak.io")
		kind := map[string]string{
			"portforwardleases": "PortForwardLease", "vpnegressroutes": "VPNEgressRoute", "vpngatewayclasses": "VPNGatewayClass",
			"vpngateways": "VPNGateway", "vpnworkloadbindings": "VPNWorkloadBinding", "workloadadapters": "WorkloadAdapter",
		}[plural]
		scope := apiextensionsv1.NamespaceScoped
		if plural == "vpngatewayclasses" {
			scope = apiextensionsv1.ClusterScoped
		}
		crds = append(crds, &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "networking.waycloak.io", Scope: scope,
				Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: plural, Kind: kind},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1beta1", Served: true, Storage: true}},
			},
		})
	}
	clients.APIExtensions = apiextensionsfake.NewSimpleClientset(crds...)
	return clients
}

func seedPortableState(t *testing.T, clients *Clients) {
	t.Helper()
	seedStateClass(t, clients)
	objects := []struct {
		kind, namespace, name string
		spec                  map[string]any
	}{
		{"WorkloadAdapter", "media", "qbittorrent", map[string]any{"image": "registry.invalid/adapter@sha256:" + strings.Repeat("a", 64), "protocolVersion": "networking.waycloak.io/adapter/v1", "supportedApplications": []any{"networking.waycloak.io/qbittorrent"}}},
		{"VPNGateway", "media", "private", map[string]any{"gatewayClassName": "gluetun.waycloak.io", "credentialRefs": []any{map[string]any{"role": "networking.waycloak.io/provider", "name": "provider-credentials"}}, "nativeConfigRefs": []any{map[string]any{"role": "networking.waycloak.io/engine", "name": "provider-config"}}}},
		{"VPNEgressRoute", "media", "private", map[string]any{"parentRefs": []any{map[string]any{"group": "networking.waycloak.io", "kind": "VPNGateway", "namespace": "media", "name": "private"}}}},
		{"PortForwardLease", "media", "torrent", map[string]any{"gatewayRef": map[string]any{"namespace": "media", "name": "private"}, "backendRef": map[string]any{"group": "", "kind": "Service", "name": "torrent", "port": int64(6881)}, "protocols": []any{"TCP", "UDP"}}},
	}
	for _, item := range objects {
		resourceType, _ := stateResourceFor(item.kind)
		object := stateObject(item.kind, item.namespace, item.name, item.spec)
		object.SetAnnotations(map[string]string{"credential-canary": "CANARY-SECRET"})
		object.SetFinalizers([]string{"runtime.example.invalid/cleanup"})
		object.Object["status"] = map[string]any{"runtimeEndpoint": "runtime-endpoint", "providerAddress": "provider-public-address"}
		if _, err := clients.Dynamic.Resource(resourceType.GVR).Namespace(item.namespace).Create(context.Background(), object, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	binding := stateObject("VPNWorkloadBinding", "media", "binding", map[string]any{"allocation": map[string]any{"identity": "binding-uid", "address": "100.96.0.2/32"}})
	if _, err := clients.Dynamic.Resource(doctorResources[3].GVR).Namespace("media").Create(context.Background(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func seedStateClass(t *testing.T, clients *Clients) {
	t.Helper()
	class := stateObject("VPNGatewayClass", "", "gluetun.waycloak.io", map[string]any{
		"controllerName": "networking.waycloak.io/gluetun", "releaseIdentity": map[string]any{"version": "v1.0.0-beta.1", "manifestDigest": "sha256:" + strings.Repeat("b", 64)},
		"supportedFeatures": []any{"networking.waycloak.io/CoreFailClosedEgress"}, "conformanceProfile": "networking.waycloak.io/Core-v1",
	})
	if _, err := clients.Dynamic.Resource(gatewayClassGVR).Create(context.Background(), class, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func stateObject(kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.waycloak.io/v1beta1", "kind": kind, "metadata": map[string]any{"name": name}, "spec": spec}}
	object.SetNamespace(namespace)
	return object
}

func namespaceObject(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func assertPortableStateCount(t *testing.T, clients *Clients, wanted int) {
	t.Helper()
	count := 0
	for _, resourceType := range portableStateResources {
		list, err := clients.Dynamic.Resource(resourceType.GVR).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		count += len(list.Items)
	}
	if count != wanted {
		t.Fatalf("portable state count=%d, want %d", count, wanted)
	}
}

func copyAnyMap(source map[string]any) map[string]any {
	data, _ := json.Marshal(source)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func reflectClusterIdentity(a, b ClusterIdentity) bool {
	return a.ServerFingerprint == b.ServerFingerprint && a.TrustFingerprint == b.TrustFingerprint && a.ClusterUIDFingerprint == b.ClusterUIDFingerprint
}
