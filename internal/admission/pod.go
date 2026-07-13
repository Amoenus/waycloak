// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type PodMutator struct {
	Client     client.Client
	Scheme     *runtime.Scheme
	AgentImage string
}

type Rejection struct{ Reason, Message string }

func (e *Rejection) Error() string { return e.Reason + ": " + e.Message }

func (m *PodMutator) Handle(ctx context.Context, req cradmission.Request) cradmission.Response {
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return cradmission.Errored(400, err)
	}
	before := req.Object.Raw
	changed, err := m.Mutate(ctx, &pod)
	if err != nil {
		if r, ok := err.(*Rejection); ok {
			return cradmission.Denied(r.Error())
		}
		return cradmission.Errored(500, err)
	}
	if !changed {
		return cradmission.Allowed("pod is unchanged")
	}
	after, err := json.Marshal(&pod)
	if err != nil {
		return cradmission.Errored(500, err)
	}
	return cradmission.PatchResponseFromRaw(before, after)
}

func (m *PodMutator) Mutate(ctx context.Context, pod *corev1.Pod) (bool, error) {
	refValue := pod.Annotations[contract.GatewayAnnotation]
	if refValue == "" {
		return false, nil
	}
	gwNamespace, gwName, err := ParseGatewayReference(pod.Namespace, refValue)
	if err != nil {
		return false, &Rejection{Reason: "InvalidGatewayReference", Message: err.Error()}
	}
	var gateway wayv1.VPNGateway
	if err := m.Client.Get(ctx, types.NamespacedName{Namespace: gwNamespace, Name: gwName}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return false, &Rejection{Reason: waystatus.ReasonGatewayNotFound, Message: fmt.Sprintf("VPNGateway %s/%s does not exist", gwNamespace, gwName)}
		}
		return false, err
	}
	var ns corev1.Namespace
	if err := m.Client.Get(ctx, types.NamespacedName{Name: pod.Namespace}, &ns); err != nil {
		return false, err
	}
	selector, err := metav1.LabelSelectorAsSelector(&gateway.Spec.WorkloadAccess.NamespaceSelector)
	if err != nil {
		return false, &Rejection{Reason: waystatus.ReasonUnauthorizedGateway, Message: "gateway namespace selector is invalid"}
	}
	if !selector.Matches(labels.Set(ns.Labels)) {
		return false, &Rejection{Reason: waystatus.ReasonUnauthorizedGateway, Message: fmt.Sprintf("namespace %q is not authorized by VPNGateway %s/%s", pod.Namespace, gwNamespace, gwName)}
	}
	if pod.Spec.HostNetwork {
		return false, &Rejection{Reason: "HostNetworkUnsupported", Message: "protected Pods cannot use hostNetwork"}
	}
	if pod.Spec.AutomountServiceAccountToken != nil && *pod.Spec.AutomountServiceAccountToken {
		return false, &Rejection{Reason: waystatus.ReasonApplicationCredentialsForbidden, Message: "protected Pods cannot automount a Kubernetes API token"}
	}
	if m.AgentImage == "" {
		return false, fmt.Errorf("agent image is not configured")
	}
	allocationName := contract.AllocationConfigMapName(pod.Namespace, pod.Name)
	if version := pod.Annotations[contract.InjectionVersionAnnotation]; version != "" {
		if version != contract.InjectionVersion {
			return false, &Rejection{Reason: waystatus.ReasonAdmissionVersionConflict, Message: fmt.Sprintf("Pod carries injection version %q, expected %q", version, contract.InjectionVersion)}
		}
		if err := validateInjected(pod, allocationName, m.AgentImage); err != nil {
			return false, &Rejection{Reason: waystatus.ReasonAdmissionVersionConflict, Message: err.Error()}
		}
		return false, nil
	}
	if hasReservedNames(pod) {
		return false, &Rejection{Reason: waystatus.ReasonAdmissionVersionConflict, Message: "Pod already uses Waycloak-reserved container or volume names"}
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[contract.InjectionVersionAnnotation] = contract.InjectionVersion
	pod.Annotations[contract.AllocationNameAnnotation] = allocationName
	f := false
	pod.Spec.AutomountServiceAccountToken = &f
	optional := false
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: contract.AllocationVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: allocationName}, Optional: &optional}}})
	mount := corev1.VolumeMount{Name: contract.AllocationVolume, MountPath: "/run/waycloak", ReadOnly: true}
	pod.Spec.InitContainers = append([]corev1.Container{injectedContainer(contract.PrepareContainer, m.AgentImage, []string{"prepare"}, mount, true), injectedContainer(contract.VerifyContainer, m.AgentImage, []string{"verify"}, mount, true)}, pod.Spec.InitContainers...)
	pod.Spec.Containers = append(pod.Spec.Containers, injectedContainer(contract.AgentContainer, m.AgentImage, []string{"run"}, mount, true))
	return true, nil
}

func injectedContainer(name, image string, args []string, mount corev1.VolumeMount, netAdmin bool) corev1.Container {
	no := false
	yes := true
	runAs := int64(65532)
	caps := &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	if netAdmin {
		caps.Add = []corev1.Capability{"NET_ADMIN"}
		runAs = 0
		no = true
		yes = false
	}
	container := corev1.Container{Name: name, Image: image, Args: args, ImagePullPolicy: corev1.PullIfNotPresent, VolumeMounts: []corev1.VolumeMount{mount}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, RunAsNonRoot: &yes, RunAsUser: &runAs, Capabilities: caps}}
	if name == contract.AgentContainer {
		container.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"/proc/1/exe", "probe"}}}, PeriodSeconds: 2, TimeoutSeconds: 1, FailureThreshold: 1, SuccessThreshold: 1}
	}
	return container
}

func ParseGatewayReference(workloadNamespace, value string) (string, string, error) {
	parts := strings.Split(value, "/")
	if len(parts) == 1 && parts[0] != "" {
		return workloadNamespace, parts[0], nil
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("gateway annotation must be <namespace>/<name> or a same-namespace name")
}

func hasReservedNames(p *corev1.Pod) bool {
	for _, v := range p.Spec.Volumes {
		if v.Name == contract.AllocationVolume {
			return true
		}
	}
	for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
		if c.Name == contract.PrepareContainer || c.Name == contract.VerifyContainer || c.Name == contract.AgentContainer {
			return true
		}
	}
	return false
}

func validateInjected(p *corev1.Pod, allocationName, image string) error {
	if p.Annotations[contract.AllocationNameAnnotation] != allocationName {
		return fmt.Errorf("allocation ConfigMap marker is not deterministic")
	}
	if p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken {
		return fmt.Errorf("service account token automount is not disabled")
	}
	var volumeOK bool
	for _, v := range p.Spec.Volumes {
		if v.Name == contract.AllocationVolume && v.ConfigMap != nil && v.ConfigMap.Name == allocationName && v.ConfigMap.Optional != nil && !*v.ConfigMap.Optional {
			volumeOK = true
		}
	}
	if !volumeOK {
		return fmt.Errorf("required UID-bound allocation ConfigMap volume is missing or optional")
	}
	want := map[string]bool{contract.PrepareContainer: false, contract.VerifyContainer: false, contract.AgentContainer: false}
	for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
		if _, ok := want[c.Name]; ok && c.Image == image {
			want[c.Name] = true
		}
	}
	for name, ok := range want {
		if !ok {
			return fmt.Errorf("required injected container %q is missing or has a conflicting image", name)
		}
	}
	return nil
}
