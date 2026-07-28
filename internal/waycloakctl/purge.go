// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	alphaGroup              = "networking.waycloak.io"
	runtimeEmptyAttestation = "protected-runtime-empty"
	runtimeGoneAttestation  = "alpha-runtime-uninstalled"
)

var alphaCRDNames = []string{
	"portforwardleases.networking.waycloak.io",
	"vpngateways.networking.waycloak.io",
	"vpnworkloads.networking.waycloak.io",
	"workloadadapters.networking.waycloak.io",
}

var alphaEnrollmentMarkers = []string{
	"networking.waycloak.io/gateway",
	"networking.waycloak.io/port-forward-container",
	"networking.waycloak.io/workload-adapter",
	"networking.waycloak.io/adapter-container",
}

type AlphaPurgeTarget struct {
	Type           string `json:"type"`
	Version        string `json:"version,omitempty"`
	Resource       string `json:"resource"`
	Namespace      string `json:"namespace,omitempty"`
	Name           string `json:"name"`
	UID            string `json:"uid"`
	FinalizerCount int    `json:"finalizerCount,omitempty"`
}

type ProtectedObject struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type AlphaPurgePlan struct {
	APIVersion              string             `json:"apiVersion"`
	Kind                    string             `json:"kind"`
	PlanID                  string             `json:"planID"`
	ServerFingerprint       string             `json:"serverFingerprint"`
	TrustFingerprint        string             `json:"trustFingerprint"`
	ClusterUIDFingerprint   string             `json:"clusterUIDFingerprint"`
	TargetDigest            string             `json:"targetDigest"`
	Targets                 []AlphaPurgeTarget `json:"targets"`
	ProtectedWorkloadOwners []ProtectedObject  `json:"protectedWorkloadOwners"`
	ProtectedPods           []ProtectedObject  `json:"protectedPods"`
	AbortBeforePurge        []string           `json:"abortBeforePurge"`
	RecoveryAfterPurge      []string           `json:"recoveryAfterPurge"`
}

type AlphaPurgeReport struct {
	APIVersion            string `json:"apiVersion"`
	Kind                  string `json:"kind"`
	PlanID                string `json:"planID"`
	ServerFingerprint     string `json:"serverFingerprint"`
	ClusterUIDFingerprint string `json:"clusterUIDFingerprint"`
	DeletedInstances      int    `json:"deletedInstances"`
	DeletedCRDs           int    `json:"deletedCRDs"`
	Complete              bool   `json:"complete"`
}

func BuildAlphaPurgePlan(ctx context.Context, clients *Clients) (AlphaPurgePlan, error) {
	plan, err := observeAlphaPurgeState(ctx, clients)
	if err != nil {
		return plan, err
	}
	plan.AbortBeforePurge = []string{
		"keep the independent quiescence fence active",
		"stop every enumerated protected workload while the old deny path remains",
		"verify the container runtime has no protected process or runnable sandbox",
		"withdraw provider mappings and clear controller finalizers before uninstall",
		"uninstall the alpha runtime separately and recheck process absence",
	}
	plan.RecoveryAfterPurge = []string{
		"keep protected workloads stopped behind the independent fence",
		"install an exact supported replacement release and author new manifests",
		"if replacement is unavailable, reinstall the independently backed-up exact alpha release without restoring runtime state",
	}
	plan.TargetDigest = targetDigest(plan.Targets)
	plan.PlanID = purgePlanID(plan)
	return plan, plan.validate()
}

func observeAlphaPurgeState(ctx context.Context, clients *Clients) (AlphaPurgePlan, error) {
	plan := AlphaPurgePlan{APIVersion: OutputAPIVersion, Kind: "AlphaPurgePlan", ServerFingerprint: clients.ClusterServerFingerprint, TrustFingerprint: clients.ClusterTrustFingerprint}
	if !validDigest(plan.ServerFingerprint) || !validDigest(plan.TrustFingerprint) {
		return plan, errors.New("trusted cluster server and CA fingerprints are required")
	}
	system, err := clients.Kubernetes.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil || system.UID == "" {
		return plan, fmt.Errorf("resolve cluster UID anchor: %w", err)
	}
	plan.ClusterUIDFingerprint = fingerprintText(string(system.UID))
	crds, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		return plan, fmt.Errorf("list alpha CRDs: %w", err)
	}
	wanted := map[string]struct{}{}
	for _, name := range alphaCRDNames {
		wanted[name] = struct{}{}
	}
	for i := range crds.Items {
		crd := &crds.Items[i]
		if _, ok := wanted[crd.Name]; !ok || crd.Spec.Group != alphaGroup {
			continue
		}
		version := servedAlphaVersion(crd)
		if version == "" {
			continue
		}
		plan.Targets = append(plan.Targets, AlphaPurgeTarget{Type: "CustomResourceDefinition", Resource: "customresourcedefinitions", Name: crd.Name, UID: string(crd.UID), FinalizerCount: len(crd.Finalizers)})
		gvr := schema.GroupVersionResource{Group: alphaGroup, Version: version, Resource: crd.Spec.Names.Plural}
		list, listErr := clients.Dynamic.Resource(gvr).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return plan, fmt.Errorf("list exact alpha target %s: %w", crd.Name, listErr)
		}
		for index := range list.Items {
			item := &list.Items[index]
			plan.Targets = append(plan.Targets, AlphaPurgeTarget{Type: "CustomResource", Version: version, Resource: crd.Spec.Names.Plural, Namespace: item.GetNamespace(), Name: item.GetName(), UID: string(item.GetUID()), FinalizerCount: len(item.GetFinalizers())})
		}
	}
	if err := observeProtectedWorkloads(ctx, clients, &plan); err != nil {
		return plan, err
	}
	sortPurgePlan(&plan)
	return plan, nil
}

func servedAlphaVersion(crd *apiextensionsv1.CustomResourceDefinition) string {
	for _, version := range crd.Spec.Versions {
		if version.Served && strings.HasPrefix(version.Name, "v1alpha") {
			return version.Name
		}
	}
	return ""
}

func observeProtectedWorkloads(ctx context.Context, clients *Clients, plan *AlphaPurgePlan) error {
	apps := clients.Kubernetes.AppsV1()
	deployments, err := apps.Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range deployments.Items {
		addProtectedOwner(plan, "Deployment", &deployments.Items[i].ObjectMeta, deployments.Items[i].Spec.Template.Annotations)
	}
	statefulSets, err := apps.StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range statefulSets.Items {
		addProtectedOwner(plan, "StatefulSet", &statefulSets.Items[i].ObjectMeta, statefulSets.Items[i].Spec.Template.Annotations)
	}
	daemonSets, err := apps.DaemonSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range daemonSets.Items {
		addProtectedOwner(plan, "DaemonSet", &daemonSets.Items[i].ObjectMeta, daemonSets.Items[i].Spec.Template.Annotations)
	}
	replicaSets, err := apps.ReplicaSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range replicaSets.Items {
		addProtectedOwner(plan, "ReplicaSet", &replicaSets.Items[i].ObjectMeta, replicaSets.Items[i].Spec.Template.Annotations)
	}
	jobs, err := clients.Kubernetes.BatchV1().Jobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range jobs.Items {
		addProtectedOwner(plan, "Job", &jobs.Items[i].ObjectMeta, jobs.Items[i].Spec.Template.Annotations)
	}
	cronJobs, err := clients.Kubernetes.BatchV1().CronJobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range cronJobs.Items {
		addProtectedOwner(plan, "CronJob", &cronJobs.Items[i].ObjectMeta, cronJobs.Items[i].Spec.JobTemplate.Spec.Template.Annotations)
	}
	pods, err := clients.Kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if hasAlphaEnrollment(pod.Annotations) {
			plan.ProtectedPods = append(plan.ProtectedPods, protectedObject("Pod", &pod.ObjectMeta))
		}
	}
	return nil
}

func addProtectedOwner(plan *AlphaPurgePlan, kind string, metadata *metav1.ObjectMeta, annotations map[string]string) {
	if hasAlphaEnrollment(annotations) {
		plan.ProtectedWorkloadOwners = append(plan.ProtectedWorkloadOwners, protectedObject(kind, metadata))
	}
}
func protectedObject(kind string, metadata *metav1.ObjectMeta) ProtectedObject {
	return ProtectedObject{Kind: kind, Namespace: metadata.Namespace, Name: metadata.Name, UID: string(metadata.UID)}
}
func hasAlphaEnrollment(annotations map[string]string) bool {
	for _, marker := range alphaEnrollmentMarkers {
		if _, ok := annotations[marker]; ok {
			return true
		}
	}
	for marker := range annotations {
		if strings.HasPrefix(marker, "internal.networking.waycloak.io/") {
			return true
		}
	}
	return false
}

func (plan AlphaPurgePlan) validate() error {
	if plan.APIVersion != OutputAPIVersion || plan.Kind != "AlphaPurgePlan" || !validDigest(plan.PlanID) || !validDigest(plan.TargetDigest) || !validDigest(plan.ServerFingerprint) || !validDigest(plan.TrustFingerprint) || !validDigest(plan.ClusterUIDFingerprint) {
		return errors.New("alpha purge plan identity is incomplete")
	}
	if plan.TargetDigest != targetDigest(plan.Targets) || plan.PlanID != purgePlanID(plan) {
		return errors.New("alpha purge plan content does not match its digest")
	}
	return nil
}

func purgePlanID(plan AlphaPurgePlan) string {
	plan.PlanID = ""
	data, _ := json.Marshal(plan)
	return digestBytes(data)
}
func targetDigest(targets []AlphaPurgeTarget) string {
	data, _ := json.Marshal(targets)
	return digestBytes(data)
}
func sortPurgePlan(plan *AlphaPurgePlan) {
	sort.Slice(plan.Targets, func(i, j int) bool { return purgeTargetKey(plan.Targets[i]) < purgeTargetKey(plan.Targets[j]) })
	sort.Slice(plan.ProtectedWorkloadOwners, func(i, j int) bool {
		return protectedKey(plan.ProtectedWorkloadOwners[i]) < protectedKey(plan.ProtectedWorkloadOwners[j])
	})
	sort.Slice(plan.ProtectedPods, func(i, j int) bool { return protectedKey(plan.ProtectedPods[i]) < protectedKey(plan.ProtectedPods[j]) })
}
func purgeTargetKey(target AlphaPurgeTarget) string {
	return target.Type + "/" + target.Version + "/" + target.Resource + "/" + target.Namespace + "/" + target.Name + "/" + target.UID
}
func protectedKey(object ProtectedObject) string {
	return object.Kind + "/" + object.Namespace + "/" + object.Name + "/" + object.UID
}

func LoadAlphaPurgePlan(path string) (AlphaPurgePlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AlphaPurgePlan{}, err
	}
	if len(data) > 4<<20 {
		return AlphaPurgePlan{}, errors.New("alpha purge plan exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan AlphaPurgePlan
	if err = decoder.Decode(&plan); err != nil {
		return plan, err
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return plan, errors.New("alpha purge plan contains trailing JSON")
	}
	return plan, plan.validate()
}

func ApplyAlphaPurgePlan(ctx context.Context, clients *Clients, plan AlphaPurgePlan, confirmation, runtimeEmpty, runtimeGone string) (AlphaPurgeReport, error) {
	report := AlphaPurgeReport{APIVersion: OutputAPIVersion, Kind: "AlphaPurgeReport", PlanID: plan.PlanID, ServerFingerprint: clients.ClusterServerFingerprint}
	if err := plan.validate(); err != nil {
		return report, err
	}
	if confirmation != plan.PlanID {
		return report, fmt.Errorf("refusing destructive purge: --confirm must exactly equal %s", plan.PlanID)
	}
	if runtimeEmpty != runtimeEmptyAttestation || runtimeGone != runtimeGoneAttestation {
		return report, errors.New("refusing destructive purge: exact runtime-empty and alpha-uninstalled attestations are required")
	}
	current, err := observeAlphaPurgeState(ctx, clients)
	if err != nil {
		return report, err
	}
	report.ClusterUIDFingerprint = current.ClusterUIDFingerprint
	if current.ServerFingerprint != plan.ServerFingerprint || current.TrustFingerprint != plan.TrustFingerprint || current.ClusterUIDFingerprint != plan.ClusterUIDFingerprint {
		return report, errors.New("cluster identity changed after purge planning")
	}
	if len(current.ProtectedPods) != 0 {
		return report, errors.New("protected Pods still exist; keep the old deny path and abort purge")
	}
	planned := map[string]AlphaPurgeTarget{}
	for _, target := range plan.Targets {
		planned[purgeTargetKey(target)] = target
	}
	for _, target := range current.Targets {
		if _, ok := planned[purgeTargetKey(target)]; !ok {
			return report, fmt.Errorf("alpha target set changed after planning: %s/%s", target.Namespace, target.Name)
		}
		if target.Type == "CustomResource" && target.FinalizerCount != 0 {
			return report, fmt.Errorf("alpha target %s/%s still has controller finalizers", target.Namespace, target.Name)
		}
	}
	for _, target := range current.Targets {
		if target.Type != "CustomResource" {
			continue
		}
		uid := types.UID(target.UID)
		err = clients.Dynamic.Resource(schema.GroupVersionResource{Group: alphaGroup, Version: target.Version, Resource: target.Resource}).Namespace(target.Namespace).Delete(ctx, target.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apierrors.IsNotFound(err) {
			return report, err
		}
		if err == nil {
			report.DeletedInstances++
		}
	}
	for _, target := range current.Targets {
		if target.Type != "CustomResourceDefinition" {
			continue
		}
		uid := types.UID(target.UID)
		err = clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, target.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apierrors.IsNotFound(err) {
			return report, err
		}
		if err == nil {
			report.DeletedCRDs++
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		remaining, observeErr := observeAlphaPurgeState(ctx, clients)
		if observeErr == nil && len(remaining.Targets) == 0 {
			report.Complete = true
			return report, nil
		}
		select {
		case <-ctx.Done():
			return report, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return report, errors.New("alpha purge did not reach an empty exact target set")
}

func runAlphaPurge(ctx context.Context, arguments []string, dependencies Dependencies) error {
	if len(arguments) == 0 {
		return errors.New("alpha-purge requires plan or apply")
	}
	switch arguments[0] {
	case "plan":
		flags := flag.NewFlagSet("alpha-purge plan", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, output := clusterFlags(flags)
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		plan, err := BuildAlphaPurgePlan(ctx, clients)
		if err != nil {
			return err
		}
		return writeOutput(dependencies.Stdout, *output, plan)
	case "apply":
		flags := flag.NewFlagSet("alpha-purge apply", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, output := clusterFlags(flags)
		path := flags.String("plan", "", "reviewed alpha purge plan JSON")
		confirmation := flags.String("confirm", "", "exact planID confirmation")
		runtimeEmpty := flags.String("attest-runtime-empty", "", "exact protected runtime absence attestation")
		runtimeGone := flags.String("attest-alpha-uninstalled", "", "exact separate alpha runtime uninstall attestation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		plan, err := LoadAlphaPurgePlan(*path)
		if err != nil {
			return err
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		report, err := ApplyAlphaPurgePlan(ctx, clients, plan, *confirmation, *runtimeEmpty, *runtimeGone)
		if writeErr := writeOutput(dependencies.Stdout, *output, report); err == nil {
			err = writeErr
		}
		return err
	default:
		return errors.New("alpha-purge requires plan or apply")
	}
}
