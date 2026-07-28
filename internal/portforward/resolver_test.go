// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResolverUsesExactServiceSlicePodAndBindingIdentity(t *testing.T) {
	lease, gateway, service := leaseFixture()
	podA, bindingA := boundPod("app-a", "pod-a", "binding-a", gateway)
	podB, bindingB := boundPod("app-b", "pod-b", "binding-b", gateway)
	sliceB := endpointSlice("slice-b", "slice-b", service, podB, 8080)
	sliceA2 := endpointSlice("slice-a2", "slice-z", service, podA, 8080)
	sliceA1 := endpointSlice("slice-a1", "slice-a", service, podA, 8080)
	resolver := Resolver{Reader: fakeReader(t, service, podA, podB, bindingA, bindingB, sliceB, sliceA2, sliceA1)}

	resolution, err := resolver.Resolve(context.Background(), lease, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidateUIDs(resolution.Candidates), []wayv1.ObjectUID{"pod-a", "pod-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate order = %v, want %v", got, want)
	}
	if resolution.Candidates[0].EndpointSliceUID != "slice-a" || resolution.Candidates[0].OverlayAddress.String() != "10.42.0.10" || resolution.Candidates[0].TargetPort != 8080 {
		t.Fatalf("exact candidate = %#v", resolution.Candidates[0])
	}
	selected, ok := Select(resolution.Candidates, nil)
	if !ok || selected.PodUID != "pod-a" {
		t.Fatalf("deterministic initial selection = %#v, %v", selected, ok)
	}
	selected, ok = Select(resolution.Candidates, &wayv1.ActiveLeaseEndpoint{PodUID: "pod-b"})
	if !ok || selected.PodUID != "pod-b" {
		t.Fatalf("sticky selection = %#v, %v", selected, ok)
	}
}

func TestResolverRejectsStaleTerminatingUnownedAndWrongGatewayEndpoints(t *testing.T) {
	lease, gateway, service := leaseFixture()
	validPod, validBinding := boundPod("valid", "valid-uid", "valid-binding", gateway)
	valid := endpointSlice("valid", "valid-slice", service, validPod, 8080)

	stalePod, staleBinding := boundPod("stale", "new-pod-uid", "stale-binding", gateway)
	stale := endpointSlice("stale", "stale-slice", service, stalePod, 8080)
	stale.Endpoints[0].TargetRef.UID = "deleted-pod-uid"

	terminatingPod, terminatingBinding := boundPod("terminating", "terminating-uid", "terminating-binding", gateway)
	terminating := endpointSlice("terminating", "terminating-slice", service, terminatingPod, 8080)
	terminating.Endpoints[0].Conditions.Terminating = boolPointer(true)

	unownedPod, unownedBinding := boundPod("unowned", "unowned-uid", "unowned-binding", gateway)
	unowned := endpointSlice("unowned", "unowned-slice", service, unownedPod, 8080)
	unowned.OwnerReferences = nil

	wrongPod, wrongBinding := boundPod("wrong", "wrong-uid", "wrong-binding", gateway)
	wrongBinding.Spec.GatewayRef.UID = "other-gateway"
	wrong := endpointSlice("wrong", "wrong-slice", service, wrongPod, 8080)

	resolver := Resolver{Reader: fakeReader(t, service,
		validPod, validBinding, valid, stalePod, staleBinding, stale,
		terminatingPod, terminatingBinding, terminating, unownedPod, unownedBinding, unowned,
		wrongPod, wrongBinding, wrong)}
	resolution, err := resolver.Resolve(context.Background(), lease, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateUIDs(resolution.Candidates); !reflect.DeepEqual(got, []wayv1.ObjectUID{"valid-uid"}) {
		t.Fatalf("unsafe candidates = %v", got)
	}
}

func TestResolverSupportsNumericPortAndClassifiesMissingPort(t *testing.T) {
	lease, gateway, service := leaseFixture()
	lease.Spec.BackendRef.Port = intstr.FromInt32(80)
	pod, binding := boundPod("app", "pod-uid", "binding-uid", gateway)
	endpoint := endpointSlice("app", "slice-uid", service, pod, 8080)
	resolver := Resolver{Reader: fakeReader(t, service, pod, binding, endpoint)}
	resolution, err := resolver.Resolve(context.Background(), lease, gateway)
	if err != nil || len(resolution.Candidates) != 1 || resolution.Candidates[0].TargetPort != 8080 {
		t.Fatalf("numeric resolution = %#v, %v", resolution, err)
	}

	lease.Spec.BackendRef.Port = intstr.FromString("missing")
	_, err = resolver.Resolve(context.Background(), lease, gateway)
	var resolutionErr *ResolutionError
	if !errors.As(err, &resolutionErr) || resolutionErr.Kind != ResolutionNotFound {
		t.Fatalf("missing port error = %#v", err)
	}
}

func leaseFixture() (*wayv1.PortForwardLease, *wayv1.VPNGateway, *corev1.Service) {
	lease := &wayv1.PortForwardLease{ObjectMeta: metav1.ObjectMeta{Name: "lease", Namespace: "apps", UID: "lease-uid"}, Spec: wayv1.PortForwardLeaseSpec{
		GatewayRef: wayv1.NamespacedObjectReference{Namespace: "network", Name: "gateway"},
		BackendRef: wayv1.ServiceBackendReference{Group: "", Kind: "Service", Name: "backend", Port: intstr.FromString("peer")},
		Protocols:  []wayv1.TransportProtocol{wayv1.ProtocolTCP, wayv1.ProtocolUDP}, EndpointPolicy: wayv1.EndpointPolicySingleActive,
	}}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "network", UID: "gateway-uid", Generation: 1}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "apps", UID: "service-uid"}, Spec: corev1.ServiceSpec{
		Selector: map[string]string{"app": "backend"}, Ports: []corev1.ServicePort{{Name: "peer", Port: 80, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}},
	}}
	return lease, gateway, service
}

func boundPod(name string, podUID types.UID, bindingUID types.UID, gateway *wayv1.VPNGateway) (*corev1.Pod, *wayv1.VPNWorkloadBinding) {
	podAddress := "192.0.2.10"
	if name == "app-b" {
		podAddress = "192.0.2.11"
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps", UID: podUID, Labels: map[string]string{EnrollmentLabel: "private"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: podAddress}}
	binding := &wayv1.VPNWorkloadBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding-" + name, Namespace: "apps", UID: bindingUID, Generation: 1}, Spec: wayv1.VPNWorkloadBindingSpec{
		PodRef: wayv1.LocalUIDReference{Name: wayv1.ObjectName(name), UID: wayv1.ObjectUID(podUID)}, RouteRef: wayv1.LocalUIDReference{Name: "private", UID: "route-uid"},
		GatewayRef: wayv1.NamespacedUIDReference{Namespace: wayv1.NamespaceName(gateway.Namespace), Name: wayv1.ObjectName(gateway.Name), UID: wayv1.ObjectUID(gateway.UID)},
		NodeName:   "node-a", Allocation: wayv1.WorkloadAllocation{Identity: "allocation-" + name, Address: allocationAddress(name)},
		Network: wayv1.WorkloadNetworkIntent{GatewayGeneration: gateway.Generation},
	}, Status: wayv1.VPNWorkloadBindingStatus{ObservedGeneration: 1, Conditions: readyBindingConditions()}}
	return pod, binding
}

func allocationAddress(name string) string {
	if name == "app-b" {
		return "10.42.0.11/32"
	}
	return "10.42.0.10/32"
}

func readyBindingConditions() wayv1.BindingConditions {
	now := metav1.NewTime(time.Unix(1000, 0).UTC())
	return wayv1.BindingConditions{
		{Type: wayv1.ConditionProgrammed, Status: metav1.ConditionTrue, Reason: wayv1.ReasonProgrammed, ObservedGeneration: 1, LastTransitionTime: now},
		{Type: wayv1.ConditionReady, Status: metav1.ConditionTrue, Reason: wayv1.ReasonReady, ObservedGeneration: 1, LastTransitionTime: now},
		{Type: wayv1.ConditionNodeReady, Status: metav1.ConditionTrue, Reason: wayv1.ReasonNodeReady, ObservedGeneration: 1, LastTransitionTime: now},
	}
}

func endpointSlice(name string, uid types.UID, service *corev1.Service, pod *corev1.Pod, port int32) *discoveryv1.EndpointSlice {
	controller := true
	portName := "peer"
	protocol := corev1.ProtocolTCP
	return &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: service.Namespace, UID: uid,
		Labels: map[string]string{discoveryv1.LabelServiceName: service.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID, Controller: &controller}}},
		AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &portName, Protocol: &protocol, Port: &port}}, Endpoints: []discoveryv1.Endpoint{{
			Addresses: []string{pod.Status.PodIP}, Conditions: discoveryv1.EndpointConditions{Ready: boolPointer(true), Serving: boolPointer(true), Terminating: boolPointer(false)},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID},
		}},
	}
}

func fakeReader(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := discoveryv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func candidateUIDs(candidates []Candidate) []wayv1.ObjectUID {
	result := make([]wayv1.ObjectUID, len(candidates))
	for index := range candidates {
		result[index] = candidates[index].PodUID
	}
	return result
}

func boolPointer(value bool) *bool { return &value }
