// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package admission

import (
	"context"
	"fmt"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func reconcileAdapterSelection(ctx context.Context, reader client.Reader, pod *corev1.Pod, mutate bool) (bool, error) {
	adapterName := pod.Annotations[contract.WorkloadAdapterAnnotation]
	containerName := pod.Annotations[contract.AdapterContainerAnnotation]
	if adapterName == "" && containerName == "" {
		return false, nil
	}
	if adapterName == "" || containerName == "" {
		return false, &Rejection{Reason: "InvalidWorkloadAdapter", Message: "workload-adapter and adapter-container annotations must be set together"}
	}
	if problems := validation.IsDNS1123Subdomain(adapterName); len(problems) != 0 {
		return false, &Rejection{Reason: "InvalidWorkloadAdapter", Message: "workload-adapter must name a valid cluster-scoped WorkloadAdapter"}
	}
	var trusted wayv1.WorkloadAdapter
	if err := reader.Get(ctx, types.NamespacedName{Name: adapterName}, &trusted); err != nil {
		if apierrors.IsNotFound(err) {
			return false, &Rejection{Reason: "WorkloadAdapterNotFound", Message: fmt.Sprintf("WorkloadAdapter %q does not exist", adapterName)}
		}
		return false, err
	}
	if trusted.Spec.ProtocolVersion != contract.AdapterProtocolVersion {
		return false, &Rejection{Reason: "UnsupportedAdapterProtocol", Message: fmt.Sprintf("WorkloadAdapter %q uses unsupported protocol %q", adapterName, trusted.Spec.ProtocolVersion)}
	}

	index := -1
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == containerName {
			index = i
			break
		}
	}
	if index == -1 {
		return false, &Rejection{Reason: "InvalidWorkloadAdapter", Message: fmt.Sprintf("adapter container %q does not exist", containerName)}
	}
	container := &pod.Spec.Containers[index]
	if container.Image != trusted.Spec.Image {
		return false, &Rejection{Reason: "UntrustedAdapterImage", Message: fmt.Sprintf("adapter container %q image does not exactly match WorkloadAdapter %q", containerName, adapterName)}
	}
	if err := validateAdapterContainer(pod, container); err != nil {
		return false, &Rejection{Reason: "UnsafeWorkloadAdapter", Message: err.Error()}
	}

	changed := false
	for _, required := range []corev1.EnvVar{
		{Name: contract.AdapterProtocolEnv, Value: contract.AdapterProtocolVersion},
		{Name: contract.AdapterLeaseEndpointEnv, Value: fmt.Sprintf("http://127.0.0.1:%d/v1/port-forward/leases", contract.AgentLeasePort)},
	} {
		found := false
		for _, existing := range container.Env {
			if existing.Name != required.Name {
				continue
			}
			found = true
			if existing.Value != required.Value || existing.ValueFrom != nil {
				return false, &Rejection{Reason: "InvalidWorkloadAdapter", Message: fmt.Sprintf("adapter container %q overrides reserved environment variable %s", containerName, required.Name)}
			}
		}
		if !found {
			if !mutate {
				return false, &Rejection{Reason: "InvalidWorkloadAdapter", Message: fmt.Sprintf("adapter container %q is missing admission-owned environment variable %s", containerName, required.Name)}
			}
			container.Env = append(container.Env, required)
			changed = true
		}
	}
	return changed, nil
}

func validateAdapterContainer(pod *corev1.Pod, container *corev1.Container) error {
	security := container.SecurityContext
	if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation || security.RunAsNonRoot == nil || !*security.RunAsNonRoot || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem {
		return fmt.Errorf("adapter container %q must explicitly disable privilege escalation and run non-root with a read-only root filesystem", container.Name)
	}
	if security.Privileged != nil && *security.Privileged {
		return fmt.Errorf("adapter container %q must not be privileged", container.Name)
	}
	if security.Capabilities == nil || len(security.Capabilities.Add) != 0 || !containsCapability(security.Capabilities.Drop, corev1.Capability("ALL")) {
		return fmt.Errorf("adapter container %q must add no capabilities and drop ALL", container.Name)
	}
	if security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault && security.SeccompProfile.Type != corev1.SeccompProfileTypeLocalhost {
		return fmt.Errorf("adapter container %q must use RuntimeDefault or Localhost seccomp", container.Name)
	}
	if container.ReadinessProbe == nil {
		return fmt.Errorf("adapter container %q must declare a readiness probe", container.Name)
	}
	if len(container.VolumeDevices) != 0 {
		return fmt.Errorf("adapter container %q must not use block devices", container.Name)
	}
	volumes := make(map[string]corev1.Volume, len(pod.Spec.Volumes))
	for _, volume := range pod.Spec.Volumes {
		volumes[volume.Name] = volume
	}
	for _, mount := range container.VolumeMounts {
		volume, ok := volumes[mount.Name]
		if !ok {
			continue
		}
		if volume.HostPath != nil {
			return fmt.Errorf("adapter container %q must not mount hostPath volume %q", container.Name, volume.Name)
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ServiceAccountToken != nil {
					return fmt.Errorf("adapter container %q must not mount a Kubernetes API token", container.Name)
				}
			}
		}
	}
	for _, port := range container.Ports {
		if port.HostPort != 0 {
			return fmt.Errorf("adapter container %q must not bind a host port", container.Name)
		}
	}
	return nil
}

func containsCapability(capabilities []corev1.Capability, wanted corev1.Capability) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}
