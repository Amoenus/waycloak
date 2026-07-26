// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package reference

import (
	"context"
	"sort"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	RouteParentIndex  = "networking.waycloak.io/route-parent"
	GatewayClassIndex = "networking.waycloak.io/gateway-class"
	LeaseGatewayIndex = "networking.waycloak.io/lease-gateway"
)

func RouteParentIndexValues(object client.Object) []string {
	route := object.(*wayv1.VPNEgressRoute)
	if len(route.Spec.ParentRefs) != 1 {
		return nil
	}
	parent := route.Spec.ParentRefs[0]
	return []string{namespacedKey(string(parent.Namespace), string(parent.Name))}
}

func GatewayClassIndexValues(object client.Object) []string {
	gateway := object.(*wayv1.VPNGateway)
	return []string{string(gateway.Spec.GatewayClassName)}
}

func LeaseGatewayIndexValues(object client.Object) []string {
	lease := object.(*wayv1.PortForwardLease)
	return []string{namespacedKey(string(lease.Spec.GatewayRef.Namespace), string(lease.Spec.GatewayRef.Name))}
}

// DependencyMapper provides the coherent dependency fan-out shared by the
// Core route controller and the Extended lease controller. The latter starts
// consuming these mappings when its replacement reconciler is implemented.
type DependencyMapper struct {
	Client client.Client
}

func (m DependencyMapper) RoutesForGateway(ctx context.Context, gateway client.Object) []reconcile.Request {
	routes := &wayv1.VPNEgressRouteList{}
	if err := m.Client.List(ctx, routes, client.MatchingFields{RouteParentIndex: namespacedKey(gateway.GetNamespace(), gateway.GetName())}); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(routes.Items))
	for i := range routes.Items {
		requests = append(requests, requestFor(&routes.Items[i]))
	}
	return SortedUniqueRequests(requests)
}

func (m DependencyMapper) LeasesForGateway(ctx context.Context, gateway client.Object) []reconcile.Request {
	leases := &wayv1.PortForwardLeaseList{}
	if err := m.Client.List(ctx, leases, client.MatchingFields{LeaseGatewayIndex: namespacedKey(gateway.GetNamespace(), gateway.GetName())}); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(leases.Items))
	for i := range leases.Items {
		requests = append(requests, requestFor(&leases.Items[i]))
	}
	return SortedUniqueRequests(requests)
}

func (m DependencyMapper) RoutesForGatewayClass(ctx context.Context, gatewayClass client.Object) []reconcile.Request {
	gateways := &wayv1.VPNGatewayList{}
	if err := m.Client.List(ctx, gateways, client.MatchingFields{GatewayClassIndex: gatewayClass.GetName()}); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for i := range gateways.Items {
		requests = append(requests, m.RoutesForGateway(ctx, &gateways.Items[i])...)
	}
	return SortedUniqueRequests(requests)
}

func (m DependencyMapper) LeasesForGatewayClass(ctx context.Context, gatewayClass client.Object) []reconcile.Request {
	gateways := &wayv1.VPNGatewayList{}
	if err := m.Client.List(ctx, gateways, client.MatchingFields{GatewayClassIndex: gatewayClass.GetName()}); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for i := range gateways.Items {
		requests = append(requests, m.LeasesForGateway(ctx, &gateways.Items[i])...)
	}
	return SortedUniqueRequests(requests)
}

func (m DependencyMapper) RoutesForNamespace(ctx context.Context, namespace client.Object) []reconcile.Request {
	routes := &wayv1.VPNEgressRouteList{}
	if err := m.Client.List(ctx, routes, client.InNamespace(namespace.GetName())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(routes.Items))
	for i := range routes.Items {
		requests = append(requests, requestFor(&routes.Items[i]))
	}
	return SortedUniqueRequests(requests)
}

func (m DependencyMapper) LeasesForNamespace(ctx context.Context, namespace client.Object) []reconcile.Request {
	leases := &wayv1.PortForwardLeaseList{}
	if err := m.Client.List(ctx, leases, client.InNamespace(namespace.GetName())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(leases.Items))
	for i := range leases.Items {
		requests = append(requests, requestFor(&leases.Items[i]))
	}
	return SortedUniqueRequests(requests)
}

func SortedUniqueRequests(requests []reconcile.Request) []reconcile.Request {
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].NamespacedName.String() < requests[j].NamespacedName.String()
	})
	if len(requests) < 2 {
		return requests
	}
	unique := requests[:1]
	for _, request := range requests[1:] {
		if request.NamespacedName != unique[len(unique)-1].NamespacedName {
			unique = append(unique, request)
		}
	}
	return unique
}

func requestFor(object client.Object) reconcile.Request {
	return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(object)}
}

func namespacedKey(namespace, name string) string {
	return namespace + "/" + name
}
