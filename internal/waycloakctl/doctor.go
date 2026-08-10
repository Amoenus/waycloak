// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	componentLabel     = "app.kubernetes.io/component"
	instanceLabel      = "app.kubernetes.io/instance"
	coreReadyNodeLabel = "networking.waycloak.io.node-restriction.kubernetes.io/core-ready"

	cniInstallerComponent = "cni-installer"
	nodeAgentComponent    = "node-agent"
)

type ConditionSummary struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

type ResourceSummary struct {
	Kind       string             `json:"kind"`
	Namespace  string             `json:"namespace,omitempty"`
	Name       string             `json:"name"`
	Generation int64              `json:"generation"`
	Conditions []ConditionSummary `json:"conditions,omitempty"`
}

type DoctorReport struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Healthy    bool              `json:"healthy"`
	Resources  []ResourceSummary `json:"resources"`
	Problems   []string          `json:"problems,omitempty"`
	Nodes      map[string]int    `json:"nodeCapabilityStates"`
}

var doctorResources = []struct {
	GVR  schema.GroupVersionResource
	Kind string
}{
	{schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpngatewayclasses"}, "VPNGatewayClass"},
	{schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpngateways"}, "VPNGateway"},
	{schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpnegressroutes"}, "VPNEgressRoute"},
	{schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpnworkloadbindings"}, "VPNWorkloadBinding"},
	{schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "portforwardleases"}, "PortForwardLease"},
	{schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "workloadadapters"}, "WorkloadAdapter"},
}

func Doctor(ctx context.Context, clients *Clients, namespace, route string) (DoctorReport, error) {
	report := DoctorReport{APIVersion: OutputAPIVersion, Kind: "DoctorReport", Healthy: true, Nodes: map[string]int{}}
	for _, resource := range doctorResources {
		list, err := clients.Dynamic.Resource(resource.GVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			report.Healthy = false
			report.Problems = append(report.Problems, resource.Kind+" observation unavailable")
			continue
		}
		for _, item := range list.Items {
			if namespace != "" && item.GetNamespace() != "" && item.GetNamespace() != namespace {
				continue
			}
			if route != "" && resource.Kind == "VPNEgressRoute" && item.GetName() != route {
				continue
			}
			summary := summarizeResource(resource.Kind, &item)
			for _, condition := range summary.Conditions {
				if condition.ObservedGeneration != summary.Generation || condition.Status != "True" && (condition.Type == "Accepted" || condition.Type == "ResolvedRefs" || condition.Type == "Programmed" || condition.Type == "Ready") {
					report.Healthy = false
					report.Problems = append(report.Problems, fmt.Sprintf("%s %s/%s: %s=%s (%s)", summary.Kind, summary.Namespace, summary.Name, condition.Type, condition.Status, condition.Reason))
				}
			}
			report.Resources = append(report.Resources, summary)
		}
	}
	nodes, err := clients.Kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return report, err
	}
	nodeSelector, selectionProblem := observeInstalledNodeSelector(ctx, clients)
	if selectionProblem != "" {
		report.Healthy = false
		report.Problems = append(report.Problems, selectionProblem)
	}
	selectedNodes := 0
	for _, node := range nodes.Items {
		if selectionProblem == "" && !nodeMatchesSelector(&node, nodeSelector) {
			report.Nodes["NotSelected"]++
			continue
		}
		selectedNodes++
		state := "Unavailable"
		if node.Labels[coreReadyNodeLabel] == "true" {
			state = "CNICapable"
		}
		report.Nodes[state]++
	}
	if len(report.Resources) == 0 {
		report.Healthy = false
		report.Problems = append(report.Problems, "No replacement Waycloak resources are observable")
	}
	if report.Nodes["Unavailable"] > 0 {
		report.Healthy = false
		report.Problems = append(report.Problems, "One or more nodes lack a current authenticated Core capability")
	}
	if selectionProblem == "" && selectedNodes == 0 {
		report.Healthy = false
		report.Problems = append(report.Problems, "No nodes match the installed Waycloak node selection")
	}
	sort.Slice(report.Resources, func(i, j int) bool {
		a, b := report.Resources[i], report.Resources[j]
		return a.Kind+"/"+a.Namespace+"/"+a.Name < b.Kind+"/"+b.Namespace+"/"+b.Name
	})
	sort.Strings(report.Problems)
	return report, nil
}

func observeInstalledNodeSelector(ctx context.Context, clients *Clients) (map[string]string, string) {
	daemonSets, err := clients.Kubernetes.AppsV1().DaemonSets(corev1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s in (%s,%s)", componentLabel, cniInstallerComponent, nodeAgentComponent),
	})
	if err != nil {
		return nil, "Waycloak node selection observation is unavailable"
	}
	installers := daemonSetsForComponent(daemonSets.Items, cniInstallerComponent)
	agents := daemonSetsForComponent(daemonSets.Items, nodeAgentComponent)
	if len(installers) != 1 || len(agents) != 1 {
		return nil, "Exactly one Waycloak CNI installer and node agent must be observable"
	}
	installer, agent := &installers[0], &agents[0]
	if installer.Namespace != agent.Namespace || installer.Labels[instanceLabel] == "" || installer.Labels[instanceLabel] != agent.Labels[instanceLabel] {
		return nil, "Waycloak CNI installer and node agent do not identify the same installation"
	}
	if !selectorsEqual(installer.Spec.Template.Spec.NodeSelector, agent.Spec.Template.Spec.NodeSelector) {
		return nil, "Waycloak CNI installer and node agent select different nodes"
	}
	return cloneSelector(installer.Spec.Template.Spec.NodeSelector), ""
}

func daemonSetsForComponent(daemonSets []appsv1.DaemonSet, component string) []appsv1.DaemonSet {
	result := make([]appsv1.DaemonSet, 0, 1)
	for i := range daemonSets {
		if daemonSets[i].Labels[componentLabel] == component {
			result = append(result, daemonSets[i])
		}
	}
	return result
}

func selectorsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func cloneSelector(selector map[string]string) map[string]string {
	result := make(map[string]string, len(selector))
	for key, value := range selector {
		result[key] = value
	}
	return result
}

func nodeMatchesSelector(node *corev1.Node, selector map[string]string) bool {
	return labels.SelectorFromSet(selector).Matches(labels.Set(node.Labels))
}

func summarizeResource(kind string, item *unstructured.Unstructured) ResourceSummary {
	summary := ResourceSummary{Kind: kind, Namespace: item.GetNamespace(), Name: item.GetName(), Generation: item.GetGeneration()}
	conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
	if kind == "VPNEgressRoute" {
		parents, _, _ := unstructured.NestedSlice(item.Object, "status", "parents")
		for _, parent := range parents {
			if object, ok := parent.(map[string]any); ok {
				nested, _, _ := unstructured.NestedSlice(object, "conditions")
				conditions = append(conditions, nested...)
			}
		}
	}
	for _, raw := range conditions {
		object, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		condition := ConditionSummary{Type: stringValue(object["type"]), Status: stringValue(object["status"]), Reason: stringValue(object["reason"])}
		condition.ObservedGeneration, _ = object["observedGeneration"].(int64)
		summary.Conditions = append(summary.Conditions, condition)
	}
	sort.Slice(summary.Conditions, func(i, j int) bool { return summary.Conditions[i].Type < summary.Conditions[j].Type })
	return summary
}

func stringValue(value any) string { result, _ := value.(string); return result }

func runDoctor(ctx context.Context, arguments []string, dependencies Dependencies) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(dependencies.Stderr)
	kubeconfig, contextName, output := clusterFlags(flags)
	namespace := flags.String("namespace", "", "optional namespace filter")
	route := flags.String("route", "", "optional route name")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *route != "" && *namespace == "" {
		return errors.New("--route requires --namespace")
	}
	clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
	if err != nil {
		return err
	}
	report, err := Doctor(ctx, clients, *namespace, *route)
	if err != nil {
		return err
	}
	if err := writeOutput(dependencies.Stdout, *output, report); err != nil {
		return err
	}
	if !report.Healthy {
		return errors.New("waycloak path is not healthy")
	}
	return nil
}
