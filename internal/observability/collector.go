// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package observability projects aggregate, privacy-bounded operational
// metrics from the Kubernetes-native Waycloak status contract. Metrics are
// never packet-programming authority; Conditions and Events remain canonical.
package observability

import (
	"context"
	"sort"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	"github.com/Amoenus/waycloak/internal/enrollment"
	"github.com/prometheus/client_golang/prometheus"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const collectTimeout = 5 * time.Second

var (
	commonConditions  = []string{wayv1.ConditionAccepted, wayv1.ConditionResolvedRefs, wayv1.ConditionProgrammed, wayv1.ConditionReady}
	gatewayConditions = append(append([]string{}, commonConditions...), wayv1.ConditionTunnelReady, wayv1.ConditionDNSReady, wayv1.ConditionMembershipApplied)
	bindingConditions = append(append([]string{}, commonConditions...), wayv1.ConditionNodeReady)
	leaseConditions   = append(append([]string{}, commonConditions...), wayv1.ConditionGatewayRulesReady, wayv1.ConditionDelivered, wayv1.ConditionAcknowledged)
	stableReasons     = map[string]struct{}{
		wayv1.ReasonAccepted: {}, wayv1.ReasonInvalid: {}, wayv1.ReasonUnsupportedClass: {},
		wayv1.ReasonUnsupportedFeature: {}, wayv1.ReasonControllerNotFound: {}, wayv1.ReasonDeleting: {},
		wayv1.ReasonResolvedRefs: {}, wayv1.ReasonInvalidRef: {}, wayv1.ReasonRefNotFound: {},
		wayv1.ReasonRefNotPermitted: {}, wayv1.ReasonIncompatibleRef: {}, wayv1.ReasonProgrammed: {},
		wayv1.ReasonPending: {}, wayv1.ReasonApplyFailed: {}, wayv1.ReasonStaleGeneration: {},
		wayv1.ReasonReady: {}, wayv1.ReasonNotReady: {}, wayv1.ReasonObservationUnavailable: {},
		wayv1.ReasonTunnelReady: {}, wayv1.ReasonTunnelNotReady: {}, wayv1.ReasonDNSReady: {},
		wayv1.ReasonDNSNotReady: {}, wayv1.ReasonMembershipApplied: {}, wayv1.ReasonMembershipPending: {},
		wayv1.ReasonNodeReady: {}, wayv1.ReasonNodeNotReady: {}, wayv1.ReasonGatewayRulesReady: {},
		wayv1.ReasonGatewayRulesPending: {}, wayv1.ReasonDelivered: {}, wayv1.ReasonDeliveryPending: {},
		wayv1.ReasonAcknowledged: {}, wayv1.ReasonAcknowledgementPending: {},
	}
)

type listReader interface {
	List(context.Context, client.ObjectList, ...client.ListOption) error
}

type Collector struct {
	reader listReader

	resourcesDesc   *prometheus.Desc
	conditionsDesc  *prometheus.Desc
	enrolledDesc    *prometheus.Desc
	allocationsDesc *prometheus.Desc
	collectionDesc  *prometheus.Desc
}

func NewCollector(reader listReader) *Collector {
	return &Collector{
		reader: reader,
		resourcesDesc: prometheus.NewDesc(
			"waycloak_resources", "Current Waycloak API objects by stable resource kind.", []string{"resource"}, nil,
		),
		conditionsDesc: prometheus.NewDesc(
			"waycloak_resource_condition_objects", "Current Waycloak objects by bounded condition state; Conditions remain authoritative.",
			[]string{"resource", "condition", "status", "reason", "current"}, nil,
		),
		enrolledDesc: prometheus.NewDesc(
			"waycloak_enrolled_pods", "Explicitly enrolled Pods by aggregate protection state.", []string{"state"}, nil,
		),
		allocationsDesc: prometheus.NewDesc(
			"waycloak_workload_allocations", "Durable workload address reservations by aggregate state.", []string{"state"}, nil,
		),
		collectionDesc: prometheus.NewDesc(
			"waycloak_metrics_collection_success", "Whether the latest aggregate collection for a stable source succeeded.", []string{"source"}, nil,
		),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.resourcesDesc
	ch <- c.conditionsDesc
	ch <- c.enrolledDesc
	ch <- c.allocationsDesc
	ch <- c.collectionDesc
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	if c.reader == nil {
		ch <- prometheus.MustNewConstMetric(c.collectionDesc, prometheus.GaugeValue, 0, "kubernetes")
		return
	}

	conditionCounts := map[conditionKey]float64{}
	resourceCounts := map[string]float64{}
	collection := map[string]float64{}

	classes := &wayv1.VPNGatewayClassList{}
	if c.list(ctx, "vpngatewayclasses", classes, collection) {
		resourceCounts["vpngatewayclass"] = float64(len(classes.Items))
		for i := range classes.Items {
			item := &classes.Items[i]
			addConditions(conditionCounts, "vpngatewayclass", item.Generation, commonConditions, item.Status.Conditions)
		}
	}
	gateways := &wayv1.VPNGatewayList{}
	if c.list(ctx, "vpngateways", gateways, collection) {
		resourceCounts["vpngateway"] = float64(len(gateways.Items))
		for i := range gateways.Items {
			item := &gateways.Items[i]
			addConditions(conditionCounts, "vpngateway", item.Generation, gatewayConditions, item.Status.Conditions)
		}
	}
	routes := &wayv1.VPNEgressRouteList{}
	if c.list(ctx, "vpnegressroutes", routes, collection) {
		resourceCounts["vpnegressroute"] = float64(len(routes.Items))
		for i := range routes.Items {
			item := &routes.Items[i]
			addConditions(conditionCounts, "vpnegressroute", item.Generation, commonConditions, item.Status.Conditions)
			for _, parent := range item.Status.Parents {
				addConditions(conditionCounts, "vpnegressroute_parent", item.Generation, commonConditions, parent.Conditions)
			}
		}
	}
	bindings := &wayv1.VPNWorkloadBindingList{}
	bindingsOK := c.list(ctx, "vpnworkloadbindings", bindings, collection)
	if bindingsOK {
		resourceCounts["vpnworkloadbinding"] = float64(len(bindings.Items))
		for i := range bindings.Items {
			item := &bindings.Items[i]
			addConditions(conditionCounts, "vpnworkloadbinding", item.Generation, bindingConditions, item.Status.Conditions)
		}
	}
	leases := &wayv1.PortForwardLeaseList{}
	if c.list(ctx, "portforwardleases", leases, collection) {
		resourceCounts["portforwardlease"] = float64(len(leases.Items))
		for i := range leases.Items {
			item := &leases.Items[i]
			addConditions(conditionCounts, "portforwardlease", item.Generation, leaseConditions, item.Status.Conditions)
		}
	}
	adapters := &wayv1.WorkloadAdapterList{}
	if c.list(ctx, "workloadadapters", adapters, collection) {
		resourceCounts["workloadadapter"] = float64(len(adapters.Items))
		for i := range adapters.Items {
			item := &adapters.Items[i]
			addConditions(conditionCounts, "workloadadapter", item.Generation, commonConditions, item.Status.Conditions)
		}
	}

	pods := &corev1.PodList{}
	podsOK := c.list(ctx, "pods", pods, collection)
	if podsOK && bindingsOK {
		for state, count := range enrolledPodStates(pods.Items, bindings.Items) {
			ch <- prometheus.MustNewConstMetric(c.enrolledDesc, prometheus.GaugeValue, count, state)
		}
	}

	reservations := &coordinationv1.LeaseList{}
	if c.list(ctx, "allocation_reservations", reservations, collection, client.MatchingLabels{waybinding.ReservationManagedByLabel: waybinding.ReservationManagedByValue}) {
		for state, count := range allocationStates(reservations.Items) {
			ch <- prometheus.MustNewConstMetric(c.allocationsDesc, prometheus.GaugeValue, count, state)
		}
	}

	for _, resource := range sortedKeys(resourceCounts) {
		ch <- prometheus.MustNewConstMetric(c.resourcesDesc, prometheus.GaugeValue, resourceCounts[resource], resource)
	}
	conditionKeys := make([]conditionKey, 0, len(conditionCounts))
	for key := range conditionCounts {
		conditionKeys = append(conditionKeys, key)
	}
	sort.Slice(conditionKeys, func(i, j int) bool { return conditionKeys[i].less(conditionKeys[j]) })
	for _, key := range conditionKeys {
		ch <- prometheus.MustNewConstMetric(c.conditionsDesc, prometheus.GaugeValue, conditionCounts[key], key.resource, key.condition, key.status, key.reason, key.current)
	}
	for _, source := range sortedKeys(collection) {
		ch <- prometheus.MustNewConstMetric(c.collectionDesc, prometheus.GaugeValue, collection[source], source)
	}
}

func (c *Collector) list(ctx context.Context, source string, list client.ObjectList, collection map[string]float64, options ...client.ListOption) bool {
	if err := c.reader.List(ctx, list, options...); err != nil {
		collection[source] = 0
		return false
	}
	collection[source] = 1
	return true
}

type conditionKey struct {
	resource, condition, status, reason, current string
}

func (k conditionKey) less(other conditionKey) bool {
	left := []string{k.resource, k.condition, k.status, k.reason, k.current}
	right := []string{other.resource, other.condition, other.status, other.reason, other.current}
	for i := range left {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return false
}

func addConditions(counts map[conditionKey]float64, resource string, generation int64, expected []string, conditions []metav1.Condition) {
	byType := make(map[string]metav1.Condition, len(conditions))
	for _, condition := range conditions {
		byType[condition.Type] = condition
	}
	for _, conditionType := range expected {
		condition, found := byType[conditionType]
		if !found {
			counts[conditionKey{resource: resource, condition: conditionType, status: "Unknown", reason: "ConditionAbsent", current: "false"}]++
			continue
		}
		reason := condition.Reason
		if _, allowed := stableReasons[reason]; !allowed {
			reason = "Other"
		}
		current := "false"
		if condition.ObservedGeneration == generation {
			current = "true"
		}
		counts[conditionKey{resource: resource, condition: conditionType, status: string(condition.Status), reason: reason, current: current}]++
	}
}

func enrolledPodStates(pods []corev1.Pod, bindings []wayv1.VPNWorkloadBinding) map[string]float64 {
	byPodUID := make(map[string]*wayv1.VPNWorkloadBinding, len(bindings))
	for i := range bindings {
		byPodUID[string(bindings[i].Spec.PodRef.UID)] = &bindings[i]
	}
	states := map[string]float64{"awaiting_capable_node": 0, "binding_absent": 0, "fail_closed": 0, "ready": 0, "terminating": 0}
	for i := range pods {
		pod := &pods[i]
		if _, enrolled := pod.Labels[enrollment.RouteLabel]; !enrolled {
			continue
		}
		if !pod.DeletionTimestamp.IsZero() {
			states["terminating"]++
			continue
		}
		if pod.Spec.NodeName == "" {
			states["awaiting_capable_node"]++
			continue
		}
		binding := byPodUID[string(pod.UID)]
		if binding == nil {
			states["binding_absent"]++
			continue
		}
		ready := false
		for _, condition := range binding.Status.Conditions {
			if condition.Type == wayv1.ConditionReady && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == binding.Generation {
				ready = true
				break
			}
		}
		if ready {
			states["ready"]++
		} else {
			states["fail_closed"]++
		}
	}
	return states
}

func allocationStates(leases []coordinationv1.Lease) map[string]float64 {
	states := map[string]float64{"active": 0, "quarantined": 0, "invalid": 0}
	for i := range leases {
		switch leases[i].Annotations[waybinding.ReservationStateAnnotation] {
		case waybinding.ReservationStateActive:
			states["active"]++
		case waybinding.ReservationStateQuarantined:
			states["quarantined"]++
		default:
			states["invalid"]++
		}
	}
	return states
}

func sortedKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
