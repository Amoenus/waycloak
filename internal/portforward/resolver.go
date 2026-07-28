// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package portforward resolves the Extended SingleActive Service backend to
// an exact, currently bound Pod UID. Services and EndpointSlices provide
// identity only; packet programming always uses the controller-owned overlay
// allocation from VPNWorkloadBinding.
package portforward

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	wayconditions "github.com/Amoenus/waycloak/internal/conditions"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const EnrollmentLabel = "networking.waycloak.io/egress-route"

type ResolutionKind string

const (
	ResolutionInvalid     ResolutionKind = "Invalid"
	ResolutionNotFound    ResolutionKind = "NotFound"
	ResolutionUnavailable ResolutionKind = "Unavailable"
)

type ResolutionError struct {
	Kind    ResolutionKind
	Message string
	Err     error
}

func (e *ResolutionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *ResolutionError) Unwrap() error { return e.Err }

type Candidate struct {
	ServiceUID         wayv1.ObjectUID
	EndpointSliceUID   wayv1.ObjectUID
	PodName            wayv1.ObjectName
	PodUID             wayv1.ObjectUID
	BindingName        wayv1.ObjectName
	BindingUID         wayv1.ObjectUID
	OverlayAddress     netip.Addr
	ApplicationAddress netip.Addr
	TargetPort         uint16
}

type Resolution struct {
	Service    *corev1.Service
	Candidates []Candidate
}

type Resolver struct {
	Reader client.Reader
}

func (r Resolver) Resolve(ctx context.Context, lease *wayv1.PortForwardLease, gateway *wayv1.VPNGateway) (Resolution, error) {
	if r.Reader == nil || lease == nil || gateway == nil {
		return Resolution{}, invalid("Lease resolver inputs are incomplete", nil)
	}
	if string(lease.Spec.BackendRef.Group) != "" || lease.Spec.BackendRef.Kind != "Service" {
		return Resolution{}, invalid("Backend must be a same-namespace core Service", nil)
	}
	service := &corev1.Service{}
	key := client.ObjectKey{Namespace: lease.Namespace, Name: string(lease.Spec.BackendRef.Name)}
	if err := r.Reader.Get(ctx, key, service); err != nil {
		if apierrors.IsNotFound(err) {
			return Resolution{}, notFound("Backend Service is unavailable", err)
		}
		return Resolution{}, unavailable("Backend Service observation is unavailable", err)
	}
	if service.Spec.Type == corev1.ServiceTypeExternalName || service.UID == "" {
		return Resolution{}, invalid("Backend Service does not provide Pod endpoint identity", nil)
	}
	servicePorts, err := matchingServicePorts(service, lease.Spec.BackendRef.Port)
	if err != nil {
		return Resolution{}, err
	}

	sliceList := &discoveryv1.EndpointSliceList{}
	if err := r.Reader.List(ctx, sliceList, client.InNamespace(lease.Namespace), client.MatchingLabels{discoveryv1.LabelServiceName: service.Name}); err != nil {
		return Resolution{}, unavailable("EndpointSlice observation is unavailable", err)
	}
	bindingList := &wayv1.VPNWorkloadBindingList{}
	if err := r.Reader.List(ctx, bindingList, client.InNamespace(lease.Namespace)); err != nil {
		return Resolution{}, unavailable("Workload binding observation is unavailable", err)
	}

	byPod := map[wayv1.ObjectUID]Candidate{}
	for sliceIndex := range sliceList.Items {
		endpointSlice := &sliceList.Items[sliceIndex]
		if !ownedByService(endpointSlice, service) || endpointSlice.AddressType != discoveryv1.AddressTypeIPv4 {
			continue
		}
		targetPort, ok := targetPortForSlice(endpointSlice, servicePorts)
		if !ok {
			continue
		}
		for endpointIndex := range endpointSlice.Endpoints {
			endpoint := &endpointSlice.Endpoints[endpointIndex]
			if !eligibleEndpoint(endpoint) {
				continue
			}
			candidate, ok, candidateErr := r.candidate(ctx, lease, gateway, service, endpointSlice, endpoint, targetPort, bindingList.Items)
			if candidateErr != nil {
				return Resolution{}, unavailable("Backend Pod observation is unavailable", candidateErr)
			}
			if !ok {
				continue
			}
			current, exists := byPod[candidate.PodUID]
			if !exists || candidate.EndpointSliceUID < current.EndpointSliceUID {
				byPod[candidate.PodUID] = candidate
			}
		}
	}
	result := Resolution{Service: service, Candidates: make([]Candidate, 0, len(byPod))}
	for _, candidate := range byPod {
		result.Candidates = append(result.Candidates, candidate)
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].PodUID == result.Candidates[j].PodUID {
			return result.Candidates[i].EndpointSliceUID < result.Candidates[j].EndpointSliceUID
		}
		return result.Candidates[i].PodUID < result.Candidates[j].PodUID
	})
	return result, nil
}

func (r Resolver) candidate(ctx context.Context, lease *wayv1.PortForwardLease, gateway *wayv1.VPNGateway, service *corev1.Service, endpointSlice *discoveryv1.EndpointSlice, endpoint *discoveryv1.Endpoint, targetPort uint16, bindings []wayv1.VPNWorkloadBinding) (Candidate, bool, error) {
	ref := endpoint.TargetRef
	if ref == nil || ref.Kind != "Pod" || ref.Name == "" || ref.UID == "" || (ref.Namespace != "" && ref.Namespace != lease.Namespace) {
		return Candidate{}, false, nil
	}
	pod := &corev1.Pod{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: lease.Namespace, Name: ref.Name}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return Candidate{}, false, nil
		}
		return Candidate{}, false, err
	}
	if pod.UID != ref.UID || pod.Status.Phase != corev1.PodRunning || !pod.DeletionTimestamp.IsZero() {
		return Candidate{}, false, nil
	}
	if len(endpoint.Addresses) != 1 {
		return Candidate{}, false, nil
	}
	applicationAddress, err := netip.ParseAddr(endpoint.Addresses[0])
	if err != nil || !applicationAddress.Is4() || pod.Status.PodIP == "" || pod.Status.PodIP != applicationAddress.String() {
		return Candidate{}, false, nil
	}
	matches := make([]*wayv1.VPNWorkloadBinding, 0, 1)
	for bindingIndex := range bindings {
		binding := &bindings[bindingIndex]
		if binding.Spec.PodRef.Name != wayv1.ObjectName(pod.Name) || binding.Spec.PodRef.UID != wayv1.ObjectUID(pod.UID) ||
			binding.Spec.GatewayRef.Namespace != wayv1.NamespaceName(gateway.Namespace) || binding.Spec.GatewayRef.Name != wayv1.ObjectName(gateway.Name) ||
			binding.Spec.GatewayRef.UID != wayv1.ObjectUID(gateway.UID) || binding.Spec.Network.GatewayGeneration != gateway.Generation || binding.UID == "" || !binding.DeletionTimestamp.IsZero() {
			continue
		}
		if pod.Labels[EnrollmentLabel] != string(binding.Spec.RouteRef.Name) ||
			!wayconditions.CurrentTrue(binding.Status.Conditions, wayv1.ConditionProgrammed, binding.Status.ObservedGeneration, binding.Generation) ||
			!wayconditions.CurrentTrue(binding.Status.Conditions, wayv1.ConditionReady, binding.Status.ObservedGeneration, binding.Generation) ||
			!wayconditions.CurrentTrue(binding.Status.Conditions, wayv1.ConditionNodeReady, binding.Status.ObservedGeneration, binding.Generation) {
			continue
		}
		matches = append(matches, binding)
	}
	if len(matches) != 1 {
		return Candidate{}, false, nil
	}
	binding := matches[0]
	prefix, err := netip.ParsePrefix(binding.Spec.Allocation.Address)
	if err != nil || !prefix.Addr().Is4() {
		return Candidate{}, false, nil
	}
	return Candidate{
		ServiceUID: wayv1.ObjectUID(service.UID), EndpointSliceUID: wayv1.ObjectUID(endpointSlice.UID),
		PodName: wayv1.ObjectName(pod.Name), PodUID: wayv1.ObjectUID(pod.UID), BindingName: wayv1.ObjectName(binding.Name),
		BindingUID: wayv1.ObjectUID(binding.UID), OverlayAddress: prefix.Addr(), ApplicationAddress: applicationAddress, TargetPort: targetPort,
	}, true, nil
}

func matchingServicePorts(service *corev1.Service, ref intstr.IntOrString) ([]corev1.ServicePort, error) {
	ports := make([]corev1.ServicePort, 0, 2)
	for _, port := range service.Spec.Ports {
		if (ref.Type == intstr.String && port.Name == ref.StrVal) || (ref.Type == intstr.Int && port.Port == ref.IntVal) {
			ports = append(ports, port)
		}
	}
	if len(ports) == 0 {
		return nil, notFound("Backend Service port is unavailable", nil)
	}
	return ports, nil
}

func targetPortForSlice(endpointSlice *discoveryv1.EndpointSlice, servicePorts []corev1.ServicePort) (uint16, bool) {
	targets := make([]int32, 0, len(servicePorts))
	for _, servicePort := range servicePorts {
		for _, endpointPort := range endpointSlice.Ports {
			if endpointPort.Port == nil {
				continue
			}
			if servicePort.Name != "" && (endpointPort.Name == nil || *endpointPort.Name != servicePort.Name) {
				continue
			}
			expected := servicePort.Port
			if servicePort.TargetPort.Type == intstr.Int && servicePort.TargetPort.IntVal != 0 {
				expected = servicePort.TargetPort.IntVal
			}
			if servicePort.TargetPort.Type == intstr.String && servicePort.TargetPort.StrVal != "" && servicePort.Name == "" {
				continue
			}
			if *endpointPort.Port != expected && servicePort.TargetPort.Type != intstr.String {
				continue
			}
			if *endpointPort.Port < 1 || *endpointPort.Port > 65535 {
				continue
			}
			targets = append(targets, *endpointPort.Port)
			break
		}
	}
	if len(targets) == 0 {
		return 0, false
	}
	for _, target := range targets[1:] {
		if target != targets[0] {
			return 0, false
		}
	}
	return uint16(targets[0]), true
}

func ownedByService(endpointSlice *discoveryv1.EndpointSlice, service *corev1.Service) bool {
	for _, owner := range endpointSlice.OwnerReferences {
		if owner.APIVersion == "v1" && owner.Kind == "Service" && owner.Name == service.Name && owner.UID == service.UID && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func eligibleEndpoint(endpoint *discoveryv1.Endpoint) bool {
	return endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready &&
		(endpoint.Conditions.Serving == nil || *endpoint.Conditions.Serving) &&
		(endpoint.Conditions.Terminating == nil || !*endpoint.Conditions.Terminating)
}

// Select deterministically preserves the current Pod UID while it remains
// eligible. A new target is chosen by Pod UID, never list or selector order.
func Select(candidates []Candidate, current *wayv1.ActiveLeaseEndpoint) (Candidate, bool) {
	if current != nil {
		for _, candidate := range candidates {
			if candidate.PodUID == current.PodUID {
				return candidate, true
			}
		}
	}
	if len(candidates) == 0 {
		return Candidate{}, false
	}
	ordered := append([]Candidate(nil), candidates...)
	slices.SortFunc(ordered, func(a, b Candidate) int {
		if a.PodUID < b.PodUID {
			return -1
		}
		if a.PodUID > b.PodUID {
			return 1
		}
		if a.EndpointSliceUID < b.EndpointSliceUID {
			return -1
		}
		if a.EndpointSliceUID > b.EndpointSliceUID {
			return 1
		}
		return 0
	})
	return ordered[0], true
}

func invalid(message string, err error) error {
	return &ResolutionError{Kind: ResolutionInvalid, Message: message, Err: err}
}

func notFound(message string, err error) error {
	return &ResolutionError{Kind: ResolutionNotFound, Message: message, Err: err}
}

func unavailable(message string, err error) error {
	if err == nil {
		err = errors.New("observation unavailable")
	}
	return &ResolutionError{Kind: ResolutionUnavailable, Message: message, Err: err}
}
