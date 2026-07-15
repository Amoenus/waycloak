// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	"github.com/Amoenus/waycloak/internal/provider"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create;update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

type GatewayReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Recorder     record.EventRecorder
	ManagerImage string
	Observer     waygateway.ManagerObserver
}

func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var gateway wayv1.VPNGateway
	if err := r.Get(ctx, req.NamespacedName, &gateway); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	previous := gateway.Status
	previous.Conditions = append([]metav1.Condition(nil), gateway.Status.Conditions...)

	if reason, message := validateGateway(&gateway, r.ManagerImage); reason != "" {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionAccepted, metav1.ConditionFalse, reason, message)
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionReady, metav1.ConditionFalse, reason, message)
		gateway.Status.ObservedGeneration = gateway.Generation
		return ctrl.Result{}, r.updateStatus(ctx, &gateway, previous)
	}

	if r.ManagerImage == "" {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionAccepted, metav1.ConditionTrue, waystatus.ReasonAccepted, "Gateway control-plane specification is accepted")
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonDataPlaneNotImplemented, "Gateway workload reconciliation is not configured")
		gateway.Status.ObservedGeneration = gateway.Generation
		return ctrl.Result{}, r.updateStatus(ctx, &gateway, previous)
	}

	desiredMembershipGeneration, err := r.reconcileResources(ctx, &gateway)
	if err != nil {
		return ctrl.Result{}, err
	}
	waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionAccepted, metav1.ConditionTrue, waystatus.ReasonGatewayResourcesReady, "Controller-owned gateway resources match the accepted specification")
	pending, err := r.observePod(ctx, &gateway, desiredMembershipGeneration)
	if err != nil {
		return ctrl.Result{}, err
	}
	gateway.Status.ObservedGeneration = gateway.Generation
	if err := r.updateStatus(ctx, &gateway, previous); err != nil {
		return ctrl.Result{}, err
	}
	if pending {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func validateGateway(gateway *wayv1.VPNGateway, managerImage string) (string, string) {
	prefix, err := netip.ParsePrefix(gateway.Spec.Overlay.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() > 30 {
		return waystatus.ReasonInvalidOverlay, "overlay.cidr must be an IPv4 prefix with client capacity"
	}
	if gateway.Spec.PortForwarding.Enabled {
		if gateway.Spec.PortForwarding.Driver != "ProtonNatPmp" {
			return waystatus.ReasonPortForwardUnsupported, "enabled port forwarding requires driver ProtonNatPmp"
		}
		if !strings.EqualFold(gateway.Spec.Provider.Name, "protonvpn") || !strings.EqualFold(gateway.Spec.Provider.Protocol, "openvpn") {
			return waystatus.ReasonPortForwardUnsupported, "ProtonNatPmp requires the protonvpn provider with OpenVPN"
		}
	}
	if managerImage == "" {
		return "", ""
	}
	if !immutableImage(gateway.Spec.Engine.Image) {
		return waystatus.ReasonInvalidEngineImage, "engine.image must be an immutable sha256 digest reference"
	}
	if !immutableImage(managerImage) {
		return waystatus.ReasonInvalidEngineImage, "configured gateway-manager image must be an immutable sha256 digest reference"
	}
	if gateway.Spec.Provider.CredentialsSecretRef.Name == "" {
		return waystatus.ReasonInvalidCredentialsReference, "provider.credentialsSecretRef.name is required"
	}
	return "", ""
}

func immutableImage(image string) bool {
	const marker = "@sha256:"
	index := strings.LastIndex(image, marker)
	if index < 1 || index+len(marker)+64 != len(image) {
		return false
	}
	_, err := hex.DecodeString(image[index+len(marker):])
	return err == nil
}

func (r *GatewayReconciler) reconcileResources(ctx context.Context, gateway *wayv1.VPNGateway) (string, error) {
	members, err := r.members(ctx, gateway)
	if err != nil {
		return "", err
	}
	portForwardLeases, err := r.portForwardLeases(ctx, gateway, members)
	if err != nil {
		return "", err
	}
	desiredConfigMap := waygateway.DesiredConfigMap(gateway, members, portForwardLeases)
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: desiredConfigMap.Name, Namespace: desiredConfigMap.Namespace}}
	operation, err := controllerutil.CreateOrUpdate(ctx, r.Client, configMap, func() error {
		configMap.Labels = desiredConfigMap.Labels
		configMap.Annotations = desiredConfigMap.Annotations
		configMap.Data = desiredConfigMap.Data
		return ctrl.SetControllerReference(gateway, configMap, r.Scheme)
	})
	if err != nil {
		return "", fmt.Errorf("reconcile gateway ConfigMap: %w", err)
	}
	if operation == controllerutil.OperationResultCreated && r.Recorder != nil {
		r.Recorder.Eventf(gateway, corev1.EventTypeNormal, "GatewayConfigCreated", "Created gateway configuration ConfigMap %s", configMap.Name)
	}

	desiredService := waygateway.DesiredService(gateway)
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: desiredService.Name, Namespace: desiredService.Namespace}}
	operation, err = controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = desiredService.Labels
		service.Annotations = desiredService.Annotations
		service.Spec = desiredService.Spec
		return ctrl.SetControllerReference(gateway, service, r.Scheme)
	})
	if err != nil {
		return "", fmt.Errorf("reconcile gateway Service: %w", err)
	}
	if operation == controllerutil.OperationResultCreated && r.Recorder != nil {
		r.Recorder.Eventf(gateway, corev1.EventTypeNormal, "GatewayServiceCreated", "Created headless gateway Service %s", service.Name)
	}

	desiredPDB := waygateway.DesiredPodDisruptionBudget(gateway)
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: desiredPDB.Name, Namespace: desiredPDB.Namespace}}
	operation, err = controllerutil.CreateOrUpdate(ctx, r.Client, pdb, func() error {
		pdb.Labels = desiredPDB.Labels
		pdb.Annotations = desiredPDB.Annotations
		pdb.Spec = desiredPDB.Spec
		return ctrl.SetControllerReference(gateway, pdb, r.Scheme)
	})
	if err != nil {
		return "", fmt.Errorf("reconcile gateway PodDisruptionBudget: %w", err)
	}
	if operation == controllerutil.OperationResultCreated && r.Recorder != nil {
		r.Recorder.Eventf(gateway, corev1.EventTypeNormal, "GatewayDisruptionBudgetCreated", "Created singleton gateway PodDisruptionBudget %s", pdb.Name)
	}

	desiredStatefulSet := waygateway.DesiredStatefulSet(gateway, waygateway.WorkloadOptions{ManagerImage: r.ManagerImage})
	statefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: desiredStatefulSet.Name, Namespace: desiredStatefulSet.Namespace}}
	operation, err = controllerutil.CreateOrUpdate(ctx, r.Client, statefulSet, func() error {
		statefulSet.Labels = desiredStatefulSet.Labels
		statefulSet.Annotations = desiredStatefulSet.Annotations
		statefulSet.Spec = desiredStatefulSet.Spec
		return ctrl.SetControllerReference(gateway, statefulSet, r.Scheme)
	})
	if err != nil {
		return "", fmt.Errorf("reconcile gateway StatefulSet: %w", err)
	}
	if operation == controllerutil.OperationResultCreated && r.Recorder != nil {
		r.Recorder.Eventf(gateway, corev1.EventTypeNormal, "GatewayStatefulSetCreated", "Created singleton gateway StatefulSet %s", statefulSet.Name)
	}
	if operation == controllerutil.OperationResultUpdated && r.Recorder != nil {
		r.Recorder.Eventf(gateway, corev1.EventTypeNormal, "GatewayRolloutRequired", "Updated singleton gateway template %s; delete its serving Pod during an approved maintenance window to activate the change", statefulSet.Name)
	}
	return desiredConfigMap.Data[waygateway.DesiredMembershipGenerationKey], nil
}

func (r *GatewayReconciler) portForwardLeases(ctx context.Context, gateway *wayv1.VPNGateway, members []waygateway.Member) ([]waygateway.PortForwardLeaseIntent, error) {
	if !gateway.Spec.PortForwarding.Enabled {
		return nil, nil
	}
	var leases wayv1.PortForwardLeaseList
	if err := r.List(ctx, &leases); err != nil {
		return nil, fmt.Errorf("list gateway port-forward leases: %w", err)
	}
	intents := make([]waygateway.PortForwardLeaseIntent, 0)
	memberAddresses := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberAddresses[member.OverlayAddress] = struct{}{}
	}
	for i := range leases.Items {
		lease := &leases.Items[i]
		namespace := lease.Spec.GatewayRef.Namespace
		if namespace == "" {
			namespace = lease.Namespace
		}
		targetReady := apiMeta.FindStatusCondition(lease.Status.Conditions, waystatus.ConditionTargetReady)
		if !lease.DeletionTimestamp.IsZero() || lease.UID == "" || namespace != gateway.Namespace || lease.Spec.GatewayRef.Name != gateway.Name || lease.Status.ProviderInternalPort < 1 || lease.Status.ProviderInternalPort > 65535 || targetReady == nil || targetReady.Status != metav1.ConditionTrue {
			continue
		}
		protocols := make([]provider.PortForwardProtocol, 0, len(lease.Spec.Protocols))
		for _, protocol := range lease.Spec.Protocols {
			protocols = append(protocols, provider.PortForwardProtocol(protocol))
		}
		sort.Slice(protocols, func(i, j int) bool { return protocols[i] < protocols[j] })
		suggested := uint16(0)
		if lease.Status.PublicPort > 0 && lease.Status.PublicPort <= 65535 {
			suggested = uint16(lease.Status.PublicPort)
		}
		if lease.Status.Target == nil || lease.Status.Target.Port < 1 || lease.Status.Target.Port > 65535 {
			continue
		}
		if _, exists := memberAddresses[lease.Status.Target.OverlayAddress]; !exists {
			continue
		}
		intents = append(intents, waygateway.PortForwardLeaseIntent{
			Identity:              string(lease.UID),
			InternalPort:          uint16(lease.Status.ProviderInternalPort),
			SuggestedExternalPort: suggested,
			Protocols:             protocols,
			TargetAddress:         lease.Status.Target.OverlayAddress,
			TargetPort:            uint16(lease.Status.Target.Port),
			LeaseGeneration:       lease.Status.LeaseGeneration,
		})
	}
	sort.Slice(intents, func(i, j int) bool { return intents[i].Identity < intents[j].Identity })
	return intents, nil
}

func (r *GatewayReconciler) members(ctx context.Context, gateway *wayv1.VPNGateway) ([]waygateway.Member, error) {
	var workloads wayv1.VPNWorkloadList
	if err := r.List(ctx, &workloads); err != nil {
		return nil, fmt.Errorf("list gateway members: %w", err)
	}
	members := make([]waygateway.Member, 0)
	for i := range workloads.Items {
		workload := &workloads.Items[i]
		if !workload.DeletionTimestamp.IsZero() || workload.Spec.GatewayRef.Namespace != gateway.Namespace || workload.Spec.GatewayRef.Name != gateway.Name || workload.Status.Allocation.Address == "" {
			continue
		}
		var pod corev1.Pod
		if err := r.Get(ctx, types.NamespacedName{Namespace: workload.Namespace, Name: workload.Spec.PodRef.Name}, &pod); err != nil {
			continue
		}
		if pod.UID != workload.Spec.PodRef.UID || pod.Status.PodIP == "" {
			continue
		}
		identity := string(workload.UID)
		if identity == "" {
			identity = workload.Namespace + "/" + workload.Name
		}
		members = append(members, waygateway.Member{ID: identity, OverlayAddress: workload.Status.Allocation.Address, UnderlayIP: pod.Status.PodIP})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return members, nil
}

func (r *GatewayReconciler) observePod(ctx context.Context, gateway *wayv1.VPNGateway, desiredMembershipGeneration string) (bool, error) {
	var previousMembership *metav1.Condition
	if existing := apiMeta.FindStatusCondition(gateway.Status.Conditions, waystatus.ConditionMembershipApplied); existing != nil {
		copy := *existing
		previousMembership = &copy
	}
	previousDesired := gateway.Status.Overlay.DesiredMembershipGeneration
	previousApplied := gateway.Status.Overlay.AppliedMembershipGeneration
	gateway.Status.Overlay = wayv1.GatewayOverlayStatus{DesiredMembershipGeneration: desiredMembershipGeneration, AppliedMembershipGeneration: previousApplied}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(gateway.Namespace), client.MatchingLabels(waygateway.SelectorLabels(gateway))); err != nil {
		return false, fmt.Errorf("list gateway Pods: %w", err)
	}
	var selected *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		if selected == nil || pod.CreationTimestamp.After(selected.CreationTimestamp.Time) {
			selected = pod
		}
	}
	if selected == nil {
		setGatewayPending(gateway, "Waiting for the controller-owned gateway Pod")
		return true, nil
	}
	managerEndpoint := ""
	if podIP, err := netip.ParseAddr(selected.Status.PodIP); err == nil {
		gateway.Status.Overlay.Endpoint = netip.AddrPortFrom(podIP.Unmap(), uint16(waygateway.VXLANPort)).String()
		gateway.Status.Overlay.HealthPort = waygateway.HealthPort
		managerEndpoint = netip.AddrPortFrom(podIP.Unmap(), uint16(waygateway.HealthPort)).String()
	}

	scheduled := podCondition(selected, corev1.PodScheduled) == corev1.ConditionTrue
	if scheduled {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionScheduled, metav1.ConditionTrue, waystatus.ReasonGatewayPodScheduled, "Gateway Pod has been scheduled")
	} else {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionScheduled, metav1.ConditionFalse, waystatus.ReasonGatewayPodPending, "Gateway Pod has not been scheduled")
	}
	managerReady := containerReady(selected, waygateway.ManagerContainer)
	membershipApplied := false
	membershipReason := waystatus.ReasonMembershipGenerationPending
	membershipMessage := "Waiting for the gateway manager to apply the desired membership generation"
	if r.Observer == nil {
		membershipReason = waystatus.ReasonMembershipObservationFailed
		membershipMessage = "Gateway manager observation is not configured"
	} else if managerEndpoint != "" {
		observation, observationErr := r.Observer.Observe(ctx, managerEndpoint)
		if observationErr != nil {
			membershipReason = waystatus.ReasonMembershipObservationFailed
			membershipMessage = observationErr.Error()
		} else {
			gateway.Status.Overlay.AppliedMembershipGeneration = observation.AppliedMembershipGeneration
			membershipApplied = observation.AppliedMembershipGeneration != "" && observation.AppliedMembershipGeneration == desiredMembershipGeneration
			if !membershipApplied {
				membershipMessage = fmt.Sprintf("Gateway manager applied membership generation %q; desired generation is %q", observation.AppliedMembershipGeneration, desiredMembershipGeneration)
			}
		}
	}
	if membershipApplied {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionMembershipApplied, metav1.ConditionTrue, waystatus.ReasonMembershipGenerationApplied, "Gateway manager has applied the desired membership generation")
	} else {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionMembershipApplied, metav1.ConditionFalse, membershipReason, membershipMessage)
		if r.Recorder != nil && (previousMembership == nil || previousMembership.Status != metav1.ConditionFalse || previousMembership.Reason != membershipReason || previousDesired != desiredMembershipGeneration || previousApplied != gateway.Status.Overlay.AppliedMembershipGeneration) {
			eventType := corev1.EventTypeNormal
			eventReason := "GatewayMembershipPending"
			if membershipReason == waystatus.ReasonMembershipObservationFailed {
				eventType = corev1.EventTypeWarning
				eventReason = "GatewayMembershipObservationFailed"
			}
			r.Recorder.Event(gateway, eventType, eventReason, membershipMessage)
		}
	}
	if managerReady {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionTunnelReady, metav1.ConditionTrue, waystatus.ReasonTunnelObservedReady, "Gateway manager reports the engine tunnel healthy")
	} else {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionTunnelReady, metav1.ConditionFalse, waystatus.ReasonTunnelNotReady, "Gateway manager has not observed a healthy engine tunnel")
	}
	if managerReady && membershipApplied {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionOverlayReady, metav1.ConditionTrue, waystatus.ReasonOverlayObservedReady, "Gateway manager reports the desired membership generation reconciled")
	} else {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionOverlayReady, metav1.ConditionFalse, waystatus.ReasonOverlayNotReady, "Gateway manager has not reported the overlay ready")
	}
	setRemainingGatewayConditions(gateway, managerReady, membershipApplied)
	return !membershipApplied, nil
}

func setGatewayPending(gateway *wayv1.VPNGateway, message string) {
	waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionScheduled, metav1.ConditionFalse, waystatus.ReasonGatewayPodPending, message)
	waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionTunnelReady, metav1.ConditionFalse, waystatus.ReasonTunnelNotReady, "No serving gateway Pod is available")
	waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionOverlayReady, metav1.ConditionFalse, waystatus.ReasonOverlayNotReady, "No serving gateway Pod is available")
	waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionMembershipApplied, metav1.ConditionFalse, waystatus.ReasonMembershipGenerationPending, "No serving gateway Pod is available")
	setRemainingGatewayConditions(gateway, false, false)
}

func setRemainingGatewayConditions(gateway *wayv1.VPNGateway, managerReady, membershipApplied bool) {
	if managerReady {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionDNSReady, metav1.ConditionTrue, waystatus.ReasonDNSObservedReady, "Gateway manager reports DNS forwarding healthy")
	} else {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionDNSReady, metav1.ConditionFalse, waystatus.ReasonDNSNotReady, "Gateway manager has not reported DNS forwarding ready")
	}
	if gateway.Spec.PortForwarding.Enabled {
		if managerReady && membershipApplied {
			waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionPortForwardReady, metav1.ConditionTrue, waystatus.ReasonPortForwardObservedReady, "Gateway manager reports provider capabilities and port-forward reconciliation healthy")
		} else {
			waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionPortForwardReady, metav1.ConditionFalse, waystatus.ReasonPortForwardNotReady, "Gateway manager has not reported port-forward reconciliation ready")
		}
	} else {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionPortForwardReady, metav1.ConditionTrue, waystatus.ReasonPortForwardDisabled, "Gateway port forwarding is disabled")
	}
	if managerReady && membershipApplied {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionReady, metav1.ConditionTrue, waystatus.ReasonGatewayReady, "All enabled gateway components are observed ready")
	} else {
		waystatus.Set(&gateway.Status.Conditions, gateway.Generation, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonGatewayComponentsNotReady, "One or more enabled gateway components are not ready")
	}
}

func podCondition(pod *corev1.Pod, conditionType corev1.PodConditionType) corev1.ConditionStatus {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status
		}
	}
	return corev1.ConditionUnknown
}

func containerReady(pod *corev1.Pod, name string) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == name {
			return status.Ready
		}
	}
	return false
}

func (r *GatewayReconciler) updateStatus(ctx context.Context, gateway *wayv1.VPNGateway, previous wayv1.VPNGatewayStatus) error {
	if apiequality.Semantic.DeepEqual(previous, gateway.Status) {
		return nil
	}
	return r.Status().Update(ctx, gateway)
}

func (r *GatewayReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		For(&wayv1.VPNGateway{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Watches(&wayv1.VPNWorkload{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []reconcile.Request {
			workload, ok := object.(*wayv1.VPNWorkload)
			if !ok || workload.Spec.GatewayRef.Name == "" || workload.Spec.GatewayRef.Namespace == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: workload.Spec.GatewayRef.Namespace, Name: workload.Spec.GatewayRef.Name}}}
		})).
		Watches(&wayv1.PortForwardLease{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []reconcile.Request {
			lease, ok := object.(*wayv1.PortForwardLease)
			if !ok || lease.Spec.GatewayRef.Name == "" {
				return nil
			}
			namespace := lease.Spec.GatewayRef.Namespace
			if namespace == "" {
				namespace = lease.Namespace
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace, Name: lease.Spec.GatewayRef.Name}}}
		})).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []reconcile.Request {
			name := object.GetAnnotations()[waygateway.GatewayNameAnnotation]
			namespace := object.GetNamespace()
			if name == "" {
				reference := object.GetAnnotations()[contract.GatewayAnnotation]
				parts := strings.Split(reference, "/")
				switch len(parts) {
				case 1:
					name = parts[0]
				case 2:
					namespace, name = parts[0], parts[1]
				}
			}
			if name == "" || namespace == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}}
		})).
		Complete(r)
}
