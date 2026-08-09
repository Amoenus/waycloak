// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const (
	portableStatePolicy      = "PortableIntentReacquire-v1"
	stateRestoreFieldManager = "waycloakctl-state-restore"
	stateRestorePlanKey      = "state.waycloak.io/restore-plan-id"
)

var portableStateExclusions = []string{
	"ConfigMap and Secret contents",
	"Pods and workload-controller objects",
	"VPNWorkloadBinding objects, allocations, and node observations",
	"conditions, status, finalizers, owner references, UIDs, and resource versions",
	"provider mappings, gateway runtime state, and live data-plane observations",
}

var portableRestoreWarnings = []string{
	"restore creates new Kubernetes UIDs and never imports bindings, allocations, provider mappings, status, or live observations",
	"restore does not create namespaces, ConfigMaps, Secrets, or workload objects",
	"keep protected workloads stopped until newly acquired bindings and the live data plane report Ready",
}

type stateResourceType struct {
	GVR   schema.GroupVersionResource
	Kind  string
	Order int
}

var portableStateResources = []stateResourceType{
	{GVR: schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "workloadadapters"}, Kind: "WorkloadAdapter", Order: 0},
	{GVR: schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpngateways"}, Kind: "VPNGateway", Order: 1},
	{GVR: schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpnegressroutes"}, Kind: "VPNEgressRoute", Order: 2},
	{GVR: schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "portforwardleases"}, Kind: "PortForwardLease", Order: 3},
}

var stateCRDNames = []string{
	"portforwardleases.networking.waycloak.io",
	"vpnegressroutes.networking.waycloak.io",
	"vpngatewayclasses.networking.waycloak.io",
	"vpngateways.networking.waycloak.io",
	"vpnworkloadbindings.networking.waycloak.io",
	"workloadadapters.networking.waycloak.io",
}

var gatewayClassGVR = schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpngatewayclasses"}

type PortableObjectMetadata struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// PortableStateResource deliberately has no status or arbitrary metadata
// field. Logical restore creates new Kubernetes identities and lets the
// replacement controllers reacquire all runtime state.
type PortableStateResource struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   PortableObjectMetadata `json:"metadata"`
	Spec       map[string]any         `json:"spec"`
}

type StateClassRequirement struct {
	Name               string `json:"name"`
	SpecDigest         string `json:"specDigest"`
	Version            string `json:"version"`
	ManifestDigest     string `json:"manifestDigest"`
	ControllerName     string `json:"controllerName"`
	ConformanceProfile string `json:"conformanceProfile"`
}

type StateBackup struct {
	APIVersion      string                  `json:"apiVersion"`
	Kind            string                  `json:"kind"`
	Policy          string                  `json:"policy"`
	BackupID        string                  `json:"backupID"`
	Source          ClusterIdentity         `json:"sourceCluster"`
	CRDIdentities   map[string]string       `json:"crdIdentities"`
	RequiredClasses []StateClassRequirement `json:"requiredClasses,omitempty"`
	Resources       []PortableStateResource `json:"resources"`
	Excluded        []string                `json:"excluded"`
}

type StateRestorePlan struct {
	APIVersion         string                  `json:"apiVersion"`
	Kind               string                  `json:"kind"`
	Policy             string                  `json:"policy"`
	PlanID             string                  `json:"planID"`
	BackupID           string                  `json:"backupID"`
	Source             ClusterIdentity         `json:"sourceCluster"`
	Target             ClusterIdentity         `json:"targetCluster"`
	TargetObservation  string                  `json:"targetObservationDigest"`
	OverlayCIDR        string                  `json:"overlayCIDR"`
	CRDIdentities      map[string]string       `json:"crdIdentities"`
	RequiredClasses    []StateClassRequirement `json:"requiredClasses,omitempty"`
	RequiredNamespaces []string                `json:"requiredNamespaces,omitempty"`
	Resources          []PortableStateResource `json:"resources"`
	Warnings           []string                `json:"warnings"`
}

func BuildStateBackup(ctx context.Context, clients *Clients) (StateBackup, error) {
	identity, err := currentClusterIdentity(ctx, clients)
	if err != nil {
		return StateBackup{}, err
	}
	crds, err := currentStateCRDIdentities(ctx, clients)
	if err != nil {
		return StateBackup{}, err
	}
	backup := StateBackup{
		APIVersion: OutputAPIVersion, Kind: "StateBackup", Policy: portableStatePolicy,
		Source: identity, CRDIdentities: crds, Excluded: append([]string(nil), portableStateExclusions...),
	}
	classNames := map[string]struct{}{}
	for _, resourceType := range portableStateResources {
		list, listErr := clients.Dynamic.Resource(resourceType.GVR).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return StateBackup{}, fmt.Errorf("list %s intent: %w", resourceType.Kind, listErr)
		}
		for index := range list.Items {
			item := &list.Items[index]
			if item.GetDeletionTimestamp() != nil {
				return StateBackup{}, fmt.Errorf("refusing transitional backup: %s %s/%s is deleting", resourceType.Kind, item.GetNamespace(), item.GetName())
			}
			spec, found, nestedErr := unstructured.NestedMap(item.Object, "spec")
			if nestedErr != nil || !found {
				return StateBackup{}, fmt.Errorf("read %s %s/%s spec", resourceType.Kind, item.GetNamespace(), item.GetName())
			}
			portable := PortableStateResource{
				APIVersion: "networking.waycloak.io/v1beta1", Kind: resourceType.Kind,
				Metadata: PortableObjectMetadata{Namespace: item.GetNamespace(), Name: item.GetName()}, Spec: spec,
			}
			if err := validatePortableStateResource(portable); err != nil {
				return StateBackup{}, err
			}
			backup.Resources = append(backup.Resources, portable)
			if resourceType.Kind == "VPNGateway" {
				className, _, _ := unstructured.NestedString(spec, "gatewayClassName")
				if className == "" {
					return StateBackup{}, fmt.Errorf("VPNGateway %s/%s has no class identity", item.GetNamespace(), item.GetName())
				}
				classNames[className] = struct{}{}
			}
		}
	}
	for name := range classNames {
		requirement, requirementErr := readStateClassRequirement(ctx, clients, name)
		if requirementErr != nil {
			return StateBackup{}, requirementErr
		}
		backup.RequiredClasses = append(backup.RequiredClasses, requirement)
	}
	sortStateBackup(&backup)
	backup.BackupID, err = backup.identityDigest()
	if err != nil {
		return StateBackup{}, err
	}
	return backup, backup.validate()
}

func BuildStateRestorePlan(ctx context.Context, clients *Clients, backup StateBackup, overlayCIDR string) (StateRestorePlan, error) {
	if err := backup.validate(); err != nil {
		return StateRestorePlan{}, err
	}
	report, err := Preflight(ctx, clients, overlayCIDR)
	if err != nil {
		return StateRestorePlan{}, err
	}
	if !report.Compatible {
		return StateRestorePlan{}, errors.New("target cluster is incompatible; no restore plan was created")
	}
	if err := verifyStatePrerequisites(ctx, clients, backup.CRDIdentities, backup.RequiredClasses, requiredStateNamespaces(backup.Resources)); err != nil {
		return StateRestorePlan{}, err
	}
	plan := StateRestorePlan{
		APIVersion: OutputAPIVersion, Kind: "StateRestorePlan", Policy: portableStatePolicy,
		BackupID: backup.BackupID, Source: backup.Source, Target: report.Identity, TargetObservation: report.ObservationDigest,
		OverlayCIDR: overlayCIDR, CRDIdentities: copyStringMap(backup.CRDIdentities),
		RequiredClasses:    append([]StateClassRequirement(nil), backup.RequiredClasses...),
		RequiredNamespaces: requiredStateNamespaces(backup.Resources), Resources: append([]PortableStateResource(nil), backup.Resources...),
		Warnings: append([]string(nil), portableRestoreWarnings...),
	}
	sortStateRestorePlan(&plan)
	plan.PlanID, err = plan.identityDigest()
	if err != nil {
		return StateRestorePlan{}, err
	}
	if err := plan.validate(); err != nil {
		return StateRestorePlan{}, err
	}
	if err := precheckStateRestoreConflicts(ctx, clients, plan); err != nil {
		return StateRestorePlan{}, err
	}
	return plan, nil
}

func ApplyStateRestorePlan(ctx context.Context, clients *Clients, plan StateRestorePlan, confirmation string) error {
	if err := plan.validate(); err != nil {
		return err
	}
	if confirmation != plan.PlanID {
		return fmt.Errorf("refusing mutation: --confirm must exactly equal %s", plan.PlanID)
	}
	report, err := Preflight(ctx, clients, plan.OverlayCIDR)
	if err != nil {
		return fmt.Errorf("re-run target preflight before mutation: %w", err)
	}
	if !report.Compatible || report.ObservationDigest != plan.TargetObservation || !reflect.DeepEqual(report.Identity, plan.Target) {
		return errors.New("refusing mutation: target cluster observation changed after restore-plan review")
	}
	if err := verifyStatePrerequisites(ctx, clients, plan.CRDIdentities, plan.RequiredClasses, plan.RequiredNamespaces); err != nil {
		return err
	}
	if err := precheckStateRestoreConflicts(ctx, clients, plan); err != nil {
		return err
	}
	for _, portable := range plan.Resources {
		resourceType, _ := stateResourceFor(portable.Kind)
		client := clients.Dynamic.Resource(resourceType.GVR).Namespace(portable.Metadata.Namespace)
		object := stateRestoreObject(portable, plan.PlanID)
		existing, getErr := client.Get(ctx, portable.Metadata.Name, metav1.GetOptions{})
		if getErr == nil {
			if stateObjectMatchesPlan(existing, portable, plan.PlanID) {
				if err := applyStateRestoreOwnership(ctx, client, object, existing.GetUID()); err != nil {
					return fmt.Errorf("restore ownership for %s %s/%s: %w", portable.Kind, portable.Metadata.Namespace, portable.Metadata.Name, err)
				}
				continue
			}
			return fmt.Errorf("refusing restore conflict: %s %s/%s is not owned by this exact plan", portable.Kind, portable.Metadata.Namespace, portable.Metadata.Name)
		}
		if !apierrors.IsNotFound(getErr) {
			return getErr
		}
		created, createErr := client.Create(ctx, object, metav1.CreateOptions{})
		if createErr != nil {
			if apierrors.IsAlreadyExists(createErr) {
				raced, getErr := client.Get(ctx, portable.Metadata.Name, metav1.GetOptions{})
				if getErr == nil && stateObjectMatchesPlan(raced, portable, plan.PlanID) {
					if err := applyStateRestoreOwnership(ctx, client, object, raced.GetUID()); err != nil {
						return fmt.Errorf("restore ownership for raced %s %s/%s: %w", portable.Kind, portable.Metadata.Namespace, portable.Metadata.Name, err)
					}
					continue
				}
				return fmt.Errorf("refusing restore race: %s %s/%s appeared before create", portable.Kind, portable.Metadata.Namespace, portable.Metadata.Name)
			}
			return fmt.Errorf("restore %s %s/%s: %w", portable.Kind, portable.Metadata.Namespace, portable.Metadata.Name, createErr)
		}
		if err := applyStateRestoreOwnership(ctx, client, object, created.GetUID()); err != nil {
			return fmt.Errorf("restore ownership for new %s %s/%s: %w", portable.Kind, portable.Metadata.Namespace, portable.Metadata.Name, err)
		}
	}
	return nil
}

func stateRestoreObject(portable PortableStateResource, planID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": portable.APIVersion,
		"kind":       portable.Kind,
		"metadata": map[string]any{
			"namespace":   portable.Metadata.Namespace,
			"name":        portable.Metadata.Name,
			"annotations": map[string]any{stateRestorePlanKey: planID},
		},
		"spec": portable.Spec,
	}}
}

func applyStateRestoreOwnership(ctx context.Context, client dynamic.ResourceInterface, desired *unstructured.Unstructured, uid types.UID) error {
	if uid == "" {
		return errors.New("API server returned an empty UID")
	}
	apply := desired.DeepCopy()
	apply.SetUID(uid)
	payload, err := json.Marshal(apply.Object)
	if err != nil {
		return fmt.Errorf("encode UID-bound server-side apply: %w", err)
	}
	if _, err = client.Patch(ctx, apply.GetName(), types.ApplyPatchType, payload, metav1.PatchOptions{FieldManager: stateRestoreFieldManager}); err != nil {
		return fmt.Errorf("UID-bound server-side apply: %w", err)
	}
	return nil
}

func LoadStateBackup(path string) (StateBackup, error) {
	var backup StateBackup
	if err := loadStrictStateFile(path, &backup); err != nil {
		return backup, err
	}
	return backup, backup.validate()
}

func LoadStateRestorePlan(path string) (StateRestorePlan, error) {
	var plan StateRestorePlan
	if err := loadStrictStateFile(path, &plan); err != nil {
		return plan, err
	}
	return plan, plan.validate()
}

func loadStrictStateFile(path string, target any) error {
	if path == "" {
		return errors.New("state file path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("state file exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("state file contains trailing JSON")
	}
	return nil
}

func (backup StateBackup) validate() error {
	if backup.APIVersion != OutputAPIVersion || backup.Kind != "StateBackup" || backup.Policy != portableStatePolicy || !validDigest(backup.BackupID) || !validClusterIdentity(backup.Source) {
		return errors.New("state backup identity is incomplete")
	}
	if err := validateStateInventory(backup.CRDIdentities, backup.RequiredClasses, backup.Resources); err != nil {
		return err
	}
	if !reflect.DeepEqual(backup.Excluded, portableStateExclusions) {
		return errors.New("state backup exclusion contract was changed")
	}
	if len(backup.Resources) > 10000 {
		return errors.New("state backup resource limit exceeded")
	}
	copy := backup
	sortStateBackup(&copy)
	if !reflect.DeepEqual(copy.Resources, backup.Resources) || !reflect.DeepEqual(copy.RequiredClasses, backup.RequiredClasses) {
		return errors.New("state backup inventory is not canonically ordered")
	}
	wanted, err := backup.identityDigest()
	if err != nil {
		return err
	}
	if backup.BackupID != wanted {
		return errors.New("state backup content does not match backupID")
	}
	encoded, err := json.Marshal(backup)
	if err != nil {
		return err
	}
	if len(encoded) > 4<<20 {
		return errors.New("state backup exceeds size limit")
	}
	return nil
}

func (backup StateBackup) identityDigest() (string, error) {
	payload := backup
	payload.BackupID = ""
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func (plan StateRestorePlan) validate() error {
	if plan.APIVersion != OutputAPIVersion || plan.Kind != "StateRestorePlan" || plan.Policy != portableStatePolicy || !validDigest(plan.PlanID) || !validDigest(plan.BackupID) || !validDigest(plan.TargetObservation) || !validClusterIdentity(plan.Source) || !validClusterIdentity(plan.Target) || plan.OverlayCIDR == "" {
		return errors.New("state restore plan identity is incomplete")
	}
	if err := validateStateInventory(plan.CRDIdentities, plan.RequiredClasses, plan.Resources); err != nil {
		return err
	}
	if !reflect.DeepEqual(plan.RequiredNamespaces, requiredStateNamespaces(plan.Resources)) {
		return errors.New("state restore namespace inventory does not match resources")
	}
	if !reflect.DeepEqual(plan.Warnings, portableRestoreWarnings) {
		return errors.New("state restore warning contract was changed")
	}
	embeddedBackup := StateBackup{
		APIVersion: OutputAPIVersion, Kind: "StateBackup", Policy: portableStatePolicy,
		BackupID: plan.BackupID, Source: plan.Source, CRDIdentities: copyStringMap(plan.CRDIdentities),
		RequiredClasses: append([]StateClassRequirement(nil), plan.RequiredClasses...),
		Resources:       append([]PortableStateResource(nil), plan.Resources...), Excluded: append([]string(nil), portableStateExclusions...),
	}
	if err := embeddedBackup.validate(); err != nil {
		return fmt.Errorf("state restore plan does not match its backup: %w", err)
	}
	copy := plan
	sortStateRestorePlan(&copy)
	if !reflect.DeepEqual(copy.Resources, plan.Resources) || !reflect.DeepEqual(copy.RequiredClasses, plan.RequiredClasses) || !reflect.DeepEqual(copy.RequiredNamespaces, plan.RequiredNamespaces) {
		return errors.New("state restore plan is not canonically ordered")
	}
	wanted, err := plan.identityDigest()
	if err != nil {
		return err
	}
	if plan.PlanID != wanted {
		return errors.New("state restore plan content does not match planID")
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if len(encoded) > 4<<20 {
		return errors.New("state restore plan exceeds size limit")
	}
	return nil
}

func (plan StateRestorePlan) identityDigest() (string, error) {
	payload := plan
	payload.PlanID = ""
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func validateStateInventory(crds map[string]string, classes []StateClassRequirement, resources []PortableStateResource) error {
	if len(crds) != len(stateCRDNames) {
		return errors.New("state inventory must contain the exact replacement CRD set")
	}
	for _, name := range stateCRDNames {
		if !validDigest(crds[name]) {
			return fmt.Errorf("state inventory lacks exact CRD identity for %s", name)
		}
	}
	classNames := map[string]struct{}{}
	for _, class := range classes {
		if len(utilvalidation.IsDNS1123Subdomain(class.Name)) != 0 || class.ControllerName == "" || class.Version == "" || class.ConformanceProfile == "" || !validDigest(class.SpecDigest) || !validDigest(class.ManifestDigest) {
			return errors.New("state class requirement is incomplete")
		}
		if _, exists := classNames[class.Name]; exists {
			return errors.New("state class requirements contain a duplicate")
		}
		classNames[class.Name] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, resource := range resources {
		if err := validatePortableStateResource(resource); err != nil {
			return err
		}
		key := stateResourceKey(resource)
		if _, exists := seen[key]; exists {
			return errors.New("state inventory contains a duplicate resource")
		}
		seen[key] = struct{}{}
		if resource.Kind == "VPNGateway" {
			className, _, _ := unstructured.NestedString(resource.Spec, "gatewayClassName")
			if _, exists := classNames[className]; !exists {
				return fmt.Errorf("state inventory lacks class requirement for VPNGateway %s/%s", resource.Metadata.Namespace, resource.Metadata.Name)
			}
		}
	}
	return nil
}

func validatePortableStateResource(resource PortableStateResource) error {
	if resource.APIVersion != "networking.waycloak.io/v1beta1" || len(utilvalidation.IsDNS1123Label(resource.Metadata.Namespace)) != 0 || len(utilvalidation.IsDNS1123Subdomain(resource.Metadata.Name)) != 0 || resource.Spec == nil {
		return errors.New("portable state resource identity is incomplete")
	}
	if _, ok := stateResourceFor(resource.Kind); !ok {
		return fmt.Errorf("portable state contains forbidden kind %q", resource.Kind)
	}
	return nil
}

func stateResourceFor(kind string) (stateResourceType, bool) {
	for _, candidate := range portableStateResources {
		if candidate.Kind == kind {
			return candidate, true
		}
	}
	return stateResourceType{}, false
}

func currentClusterIdentity(ctx context.Context, clients *Clients) (ClusterIdentity, error) {
	identity := ClusterIdentity{ServerFingerprint: clients.ClusterServerFingerprint, TrustFingerprint: clients.ClusterTrustFingerprint}
	if !validDigest(identity.ServerFingerprint) || !validDigest(identity.TrustFingerprint) {
		return identity, errors.New("trusted cluster server and CA fingerprints are required")
	}
	system, err := clients.Kubernetes.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return identity, fmt.Errorf("resolve cluster UID anchor: %w", err)
	}
	if system.UID == "" {
		return identity, errors.New("resolve cluster UID anchor: kube-system UID is empty")
	}
	identity.ClusterUIDFingerprint = fingerprintText(string(system.UID))
	return identity, nil
}

func validClusterIdentity(identity ClusterIdentity) bool {
	return validDigest(identity.ServerFingerprint) && validDigest(identity.TrustFingerprint) && validDigest(identity.ClusterUIDFingerprint)
}

func currentStateCRDIdentities(ctx context.Context, clients *Clients) (map[string]string, error) {
	identities := make(map[string]string, len(stateCRDNames))
	for _, name := range stateCRDNames {
		crd, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("resolve replacement CRD %s: %w", name, err)
		}
		data, err := json.Marshal(crd.Spec)
		if err != nil {
			return nil, err
		}
		identities[name] = digestBytes(data)
	}
	return identities, nil
}

func readStateClassRequirement(ctx context.Context, clients *Clients, name string) (StateClassRequirement, error) {
	class, err := clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return StateClassRequirement{}, fmt.Errorf("resolve required VPNGatewayClass %s: %w", name, err)
	}
	spec, found, err := unstructured.NestedMap(class.Object, "spec")
	if err != nil || !found {
		return StateClassRequirement{}, fmt.Errorf("resolve required VPNGatewayClass %s spec", name)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return StateClassRequirement{}, err
	}
	version, _, _ := unstructured.NestedString(spec, "releaseIdentity", "version")
	manifest, _, _ := unstructured.NestedString(spec, "releaseIdentity", "manifestDigest")
	controller, _, _ := unstructured.NestedString(spec, "controllerName")
	profile, _, _ := unstructured.NestedString(spec, "conformanceProfile")
	requirement := StateClassRequirement{Name: name, SpecDigest: digestBytes(data), Version: version, ManifestDigest: manifest, ControllerName: controller, ConformanceProfile: profile}
	if !validDigest(requirement.SpecDigest) || !validDigest(requirement.ManifestDigest) || requirement.Version == "" || requirement.ControllerName == "" || requirement.ConformanceProfile == "" {
		return StateClassRequirement{}, fmt.Errorf("required VPNGatewayClass %s has incomplete release identity", name)
	}
	return requirement, nil
}

func verifyStatePrerequisites(ctx context.Context, clients *Clients, crds map[string]string, classes []StateClassRequirement, namespaces []string) error {
	currentCRDs, err := currentStateCRDIdentities(ctx, clients)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(currentCRDs, crds) {
		return errors.New("target replacement CRD identities do not match the backup")
	}
	for _, wanted := range classes {
		current, err := readStateClassRequirement(ctx, clients, wanted.Name)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, wanted) {
			return fmt.Errorf("target VPNGatewayClass %s does not match the backup release identity", wanted.Name)
		}
	}
	for _, namespace := range namespaces {
		if _, err := clients.Kubernetes.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("required target namespace %s is unavailable: %w", namespace, err)
		}
	}
	return nil
}

func precheckStateRestoreConflicts(ctx context.Context, clients *Clients, plan StateRestorePlan) error {
	for _, portable := range plan.Resources {
		resourceType, _ := stateResourceFor(portable.Kind)
		existing, err := clients.Dynamic.Resource(resourceType.GVR).Namespace(portable.Metadata.Namespace).Get(ctx, portable.Metadata.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !stateObjectMatchesPlan(existing, portable, plan.PlanID) {
			return fmt.Errorf("refusing restore conflict: %s %s/%s already exists", portable.Kind, portable.Metadata.Namespace, portable.Metadata.Name)
		}
	}
	return nil
}

func stateObjectMatchesPlan(existing *unstructured.Unstructured, portable PortableStateResource, planID string) bool {
	if existing.GetAnnotations()[stateRestorePlanKey] != planID {
		return false
	}
	spec, found, err := unstructured.NestedMap(existing.Object, "spec")
	return err == nil && found && reflect.DeepEqual(spec, portable.Spec)
}

func requiredStateNamespaces(resources []PortableStateResource) []string {
	set := map[string]struct{}{}
	for _, resource := range resources {
		set[resource.Metadata.Namespace] = struct{}{}
	}
	namespaces := make([]string, 0, len(set))
	for namespace := range set {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces
}

func sortStateBackup(backup *StateBackup) {
	sort.Slice(backup.RequiredClasses, func(i, j int) bool { return backup.RequiredClasses[i].Name < backup.RequiredClasses[j].Name })
	sort.Slice(backup.Resources, func(i, j int) bool {
		return stateResourceKey(backup.Resources[i]) < stateResourceKey(backup.Resources[j])
	})
}

func sortStateRestorePlan(plan *StateRestorePlan) {
	sort.Slice(plan.RequiredClasses, func(i, j int) bool { return plan.RequiredClasses[i].Name < plan.RequiredClasses[j].Name })
	sort.Slice(plan.Resources, func(i, j int) bool { return stateResourceKey(plan.Resources[i]) < stateResourceKey(plan.Resources[j]) })
	sort.Strings(plan.RequiredNamespaces)
}

func stateResourceKey(resource PortableStateResource) string {
	resourceType, _ := stateResourceFor(resource.Kind)
	return fmt.Sprintf("%02d/%s/%s/%s", resourceType.Order, resource.Kind, resource.Metadata.Namespace, resource.Metadata.Name)
}

func copyStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
