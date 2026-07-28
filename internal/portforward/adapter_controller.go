// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	wayconditions "github.com/Amoenus/waycloak/internal/conditions"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var adapterConditionOrder = []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady}

type WorkloadAdapterReconciler struct {
	client.Client
	APIReader client.Reader
	Health    AdapterHealthChecker
	Now       func() time.Time
}

func (r *WorkloadAdapterReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	adapter := &wayv1.WorkloadAdapter{}
	if err := r.Get(ctx, request.NamespacedName, adapter); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	states := map[string]wayconditions.State{
		wayv1.ConditionAccepted:     wayconditions.True(wayv1.ReasonAccepted, "Adapter trust record is accepted"),
		wayv1.ConditionResolvedRefs: wayconditions.False(wayv1.ReasonRefNotFound, "Adapter Service endpoint is unresolved"),
		wayv1.ConditionProgrammed:   wayconditions.False(wayv1.ReasonPending, "Adapter process is unavailable"),
		wayv1.ConditionReady:        wayconditions.False(wayv1.ReasonNotReady, "Adapter protocol is not ready"),
	}
	pod, resolutionErr := r.resolvePod(ctx, adapter)
	switch {
	case resolutionErr != nil:
		states[wayv1.ConditionResolvedRefs] = wayconditions.Unknown("Adapter endpoint observation is unavailable")
		states[wayv1.ConditionProgrammed] = wayconditions.Unknown("Adapter process observation is unavailable")
		states[wayv1.ConditionReady] = wayconditions.Unknown("Adapter protocol observation is unavailable")
	case pod == nil:
	case !safeAdapterPod(pod, adapter.Spec.Image):
		states[wayv1.ConditionResolvedRefs] = wayconditions.False(wayv1.ReasonIncompatibleRef, "Adapter Pod does not satisfy the credential-free runtime contract")
	case r.Health == nil:
		states[wayv1.ConditionResolvedRefs] = wayconditions.True(wayv1.ReasonResolvedRefs, "Exact adapter Service endpoint is resolved")
		states[wayv1.ConditionProgrammed] = wayconditions.True(wayv1.ReasonProgrammed, "Unprivileged adapter process is available")
		states[wayv1.ConditionReady] = wayconditions.Unknown("Adapter protocol observation is unavailable")
	default:
		states[wayv1.ConditionResolvedRefs] = wayconditions.True(wayv1.ReasonResolvedRefs, "Exact adapter Service endpoint is resolved")
		states[wayv1.ConditionProgrammed] = wayconditions.True(wayv1.ReasonProgrammed, "Unprivileged adapter process is available")
		observation, err := r.Health.Observe(ctx, wayv1.NamespaceName(adapter.Namespace), wayv1.ObjectName(adapter.Name), adapter.Spec.Image)
		if err != nil || observation.PodUID != wayv1.ObjectUID(pod.UID) || observation.ObservedAt.Before(r.now().Add(-DefaultObservationFreshness)) || observation.ObservedAt.After(r.now().Add(time.Minute)) {
			states[wayv1.ConditionReady] = wayconditions.Unknown("Adapter protocol observation is unavailable")
		} else if observation.Ready {
			states[wayv1.ConditionReady] = wayconditions.True(wayv1.ReasonReady, "Adapter protocol is ready")
		}
	}
	status := wayv1.WorkloadAdapterStatus{ObservedGeneration: adapter.Generation,
		Conditions: wayconditions.Build(adapter.Status.Conditions, adapter.Generation, r.now(), adapterConditionOrder, states)}
	if !reflect.DeepEqual(adapter.Status, status) {
		if err := r.applyStatus(ctx, adapter, status); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: DefaultObservationFreshness / 2}, nil
}

func (r *WorkloadAdapterReconciler) resolvePod(ctx context.Context, adapter *wayv1.WorkloadAdapter) (*corev1.Pod, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	service := &corev1.Service{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: adapter.Namespace, Name: AdapterServiceName(wayv1.NamespaceName(adapter.Namespace), wayv1.ObjectName(adapter.Name))}, service); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if service.UID == "" || service.Spec.Type == corev1.ServiceTypeExternalName || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Protocol != corev1.ProtocolTCP || service.Spec.Ports[0].Port != int32(DefaultAdapterPort) {
		return nil, nil
	}
	slices := &discoveryv1.EndpointSliceList{}
	if err := reader.List(ctx, slices, client.InNamespace(adapter.Namespace), client.MatchingLabels{discoveryv1.LabelServiceName: service.Name}); err != nil {
		return nil, err
	}
	pods := map[types.UID]*corev1.Pod{}
	for sliceIndex := range slices.Items {
		endpointSlice := &slices.Items[sliceIndex]
		if !ownedByService(endpointSlice, service) || !adapterSlicePort(endpointSlice) {
			continue
		}
		for endpointIndex := range endpointSlice.Endpoints {
			endpoint := &endpointSlice.Endpoints[endpointIndex]
			if !eligibleEndpoint(endpoint) || endpoint.TargetRef == nil || endpoint.TargetRef.Kind != "Pod" || endpoint.TargetRef.Name == "" || endpoint.TargetRef.UID == "" {
				continue
			}
			pod := &corev1.Pod{}
			if err := reader.Get(ctx, client.ObjectKey{Namespace: adapter.Namespace, Name: endpoint.TargetRef.Name}, pod); err != nil {
				return nil, err
			}
			if pod.UID == endpoint.TargetRef.UID && pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp.IsZero() {
				pods[pod.UID] = pod
			}
		}
	}
	if len(pods) != 1 {
		return nil, nil
	}
	for _, pod := range pods {
		return pod, nil
	}
	return nil, nil
}

func safeAdapterPod(pod *corev1.Pod, image string) bool {
	if pod == nil || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC || pod.Spec.ShareProcessNamespace != nil && *pod.Spec.ShareProcessNamespace ||
		pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 || len(pod.Spec.Containers) != 1 {
		return false
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath != nil || volume.Projected != nil && projectedServiceAccountToken(volume.Projected) {
			return false
		}
	}
	container := pod.Spec.Containers[0]
	security := container.SecurityContext
	if container.Image != image || len(container.Ports) != 1 || container.Ports[0].ContainerPort != int32(DefaultAdapterPort) || container.Ports[0].Protocol != corev1.ProtocolTCP || security == nil || security.Privileged != nil && *security.Privileged || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.RunAsNonRoot == nil || !*security.RunAsNonRoot || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem || security.Capabilities == nil || len(security.Capabilities.Add) != 0 || !containsCapability(security.Capabilities.Drop, corev1.Capability("ALL")) {
		return false
	}
	seccomp := security.SeccompProfile
	if seccomp == nil && pod.Spec.SecurityContext != nil {
		seccomp = pod.Spec.SecurityContext.SeccompProfile
	}
	return seccomp != nil && seccomp.Type == corev1.SeccompProfileTypeRuntimeDefault
}

func adapterSlicePort(endpointSlice *discoveryv1.EndpointSlice) bool {
	for _, port := range endpointSlice.Ports {
		if port.Port != nil && *port.Port == int32(DefaultAdapterPort) && (port.Protocol == nil || *port.Protocol == corev1.ProtocolTCP) {
			return true
		}
	}
	return false
}

func projectedServiceAccountToken(projected *corev1.ProjectedVolumeSource) bool {
	for _, source := range projected.Sources {
		if source.ServiceAccountToken != nil {
			return true
		}
	}
	return false
}

func containsCapability(values []corev1.Capability, want corev1.Capability) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (r *WorkloadAdapterReconciler) applyStatus(ctx context.Context, adapter *wayv1.WorkloadAdapter, status wayv1.WorkloadAdapterStatus) error {
	apply := &wayv1.WorkloadAdapter{TypeMeta: metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "WorkloadAdapter"},
		ObjectMeta: metav1.ObjectMeta{Name: adapter.Name, Namespace: adapter.Namespace}, Status: status}
	data, err := json.Marshal(apply)
	if err != nil {
		return err
	}
	if err := r.SubResource("status").Patch(ctx, apply, client.RawPatch(types.ApplyPatchType, data), client.FieldOwner(wayv1.FieldManagerAdapterController), client.ForceOwnership); err != nil {
		return fmt.Errorf("apply WorkloadAdapter status: %w", err)
	}
	return nil
}

func (r *WorkloadAdapterReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&wayv1.WorkloadAdapter{}).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.adaptersInNamespace)).
		Watches(&discoveryv1.EndpointSlice{}, handler.EnqueueRequestsFromMapFunc(r.adaptersInNamespace)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.adaptersInNamespace)).
		Complete(r)
}

func (r *WorkloadAdapterReconciler) adaptersInNamespace(ctx context.Context, object client.Object) []reconcile.Request {
	adapters := &wayv1.WorkloadAdapterList{}
	if err := r.List(ctx, adapters, client.InNamespace(object.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(adapters.Items))
	for index := range adapters.Items {
		adapter := &adapters.Items[index]
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(adapter)})
	}
	return requests
}

func (r *WorkloadAdapterReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

var _ reconcile.Reconciler = (*WorkloadAdapterReconciler)(nil)
