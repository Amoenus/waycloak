// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

var transitionBindingGVR = schema.GroupVersionResource{
	Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpnworkloadbindings",
}

// ensureTransitionQuiescence replaces only the node-agent executable with the
// reviewed successor while retaining the source release identity. The static
// hold makes that successor install and continually retain deny-first state.
// The class, controller, CNI, gateway, and Helm release remain the exact source
// until every scheduled agent and every published binding acknowledge the hold.
func ensureTransitionQuiescence(ctx context.Context, clients *Clients, plan InstallPlan) error {
	components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return fmt.Errorf("observe pre-transition quiescence boundary: %w", err)
	}
	expectedCheckpoint := installCheckpointQuiesced
	alreadyQuiesced := exactQuiescedComponents(components, plan, false)
	validSource := exactSourceComponents(components, plan.Source, false)
	if !components.ClassPresent {
		expectedCheckpoint = installCheckpointQuiescedClassWithdrawn
		alreadyQuiesced = exactQuiescedComponents(components, plan, true)
		validSource = exactSourceComponents(components, plan.Source, true)
	} else if exactClassReplacedComponents(components, plan.Source, plan.Target) || exactQuiescedClassReplacedComponents(components, plan) {
		expectedCheckpoint = installCheckpointQuiescedClassReplaced
		alreadyQuiesced = exactQuiescedClassReplacedComponents(components, plan)
		validSource = exactClassReplacedComponents(components, plan.Source, plan.Target)
	}
	if alreadyQuiesced {
		return waitForTransitionQuiescence(ctx, clients, plan, expectedCheckpoint)
	}
	if !validSource {
		return errors.New("refusing transition quiescence outside the exact journal-bound source checkpoint")
	}

	agents := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace)
	name := chartFullname(plan.Release) + "-node-agent"
	agent, err := agents.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read exact source node agent for transition hold: %w", err)
	}
	container, index, err := mutableRequiredContainer(agent.Spec.Template.Spec.Containers, "node-agent")
	if err != nil {
		return err
	}
	if held, holdErr := observationCapabilityHeld(container.Args); holdErr != nil || held {
		if holdErr != nil {
			return holdErr
		}
		return errors.New("source node agent already contains an unbound transition hold")
	}
	artifact, ok := plan.Target.Images["waycloak-node-agent"]
	if !ok || artifact.Repository == "" || !validDigest(artifact.Digest) {
		return errors.New("reviewed target lacks an exact node-agent image")
	}
	agent = agent.DeepCopy()
	agent.Spec.Template.Spec.Containers[index].Image = artifact.Repository + "@" + artifact.Digest
	agent.Spec.Template.Spec.Containers[index].Args = append(
		append([]string(nil), agent.Spec.Template.Spec.Containers[index].Args...),
		"--observation-capability-hold=true",
		"--observation-capability-hold-id="+plan.PlanID,
	)
	if agent.Spec.Template.Annotations == nil {
		agent.Spec.Template.Annotations = map[string]string{}
	}
	if existing := agent.Spec.Template.Annotations[installTransitionPlanKey]; existing != "" && existing != plan.PlanID {
		return errors.New("source node agent contains a foreign transition hold")
	}
	agent.Spec.Template.Annotations[installTransitionPlanKey] = plan.PlanID
	if _, err = agents.Update(ctx, agent, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("install exact transition deny hold: %w", err)
	}
	return waitForTransitionQuiescence(ctx, clients, plan, expectedCheckpoint)
}

func waitForTransitionQuiescence(ctx context.Context, clients *Clients, plan InstallPlan, expectedCheckpoint string) error {
	err := wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		components, observeErr := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
		if observeErr != nil {
			return false, nil
		}
		exact := exactQuiescedComponents(components, plan, false)
		if expectedCheckpoint == installCheckpointQuiescedClassWithdrawn {
			exact = exactQuiescedComponents(components, plan, true)
		} else if expectedCheckpoint == installCheckpointQuiescedClassReplaced {
			exact = exactQuiescedClassReplacedComponents(components, plan)
		}
		if !exact {
			return false, nil
		}
		agent, getErr := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, chartFullname(plan.Release)+"-node-agent", metav1.GetOptions{})
		if getErr != nil {
			return false, getErr
		}
		status := agent.Status
		if status.DesiredNumberScheduled < 1 || status.ObservedGeneration < agent.Generation ||
			status.UpdatedNumberScheduled != status.DesiredNumberScheduled || status.NumberReady != status.DesiredNumberScheduled ||
			status.NumberAvailable != status.DesiredNumberScheduled || status.NumberUnavailable != 0 {
			return false, nil
		}
		bindings, listErr := clients.Dynamic.Resource(transitionBindingGVR).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return false, listErr
		}
		for index := range bindings.Items {
			if !bindingAcknowledgesTransitionDeny(&bindings.Items[index], plan.PlanID) {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("wait for every node attachment to acknowledge transition deny: %w", err)
	}
	return nil
}

func bindingAcknowledgesTransitionDeny(binding *unstructured.Unstructured, planID string) bool {
	observedGeneration, found, _ := unstructured.NestedInt64(binding.Object, "status", "observedGeneration")
	if !found || observedGeneration != binding.GetGeneration() {
		return false
	}
	appliedGeneration, _, _ := unstructured.NestedInt64(binding.Object, "status", "appliedGeneration")
	if appliedGeneration != 0 {
		return false
	}
	instanceID, found, _ := unstructured.NestedString(binding.Object, "status", "agent", "instanceID")
	if !found || instanceID != planID {
		return false
	}
	conditions, _, _ := unstructured.NestedSlice(binding.Object, "status", "conditions")
	required := map[string]bool{"Programmed": false, "Ready": false, "NodeReady": false}
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _, _ := unstructured.NestedString(condition, "type")
		status, _, _ := unstructured.NestedString(condition, "status")
		if _, tracked := required[conditionType]; tracked {
			if status == string(corev1.ConditionTrue) {
				return false
			}
			required[conditionType] = true
		}
	}
	return required["Programmed"] && required["Ready"] && required["NodeReady"]
}

func mutableRequiredContainer(containers []corev1.Container, name string) (corev1.Container, int, error) {
	for index := range containers {
		if containers[index].Name == name {
			return containers[index], index, nil
		}
	}
	return corev1.Container{}, -1, fmt.Errorf("deployed release lacks %s container", name)
}
