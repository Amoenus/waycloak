// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waybinding "github.com/Amoenus/waycloak/internal/binding"
	waycni "github.com/Amoenus/waycloak/internal/cni"
	"github.com/Amoenus/waycloak/internal/dataplane"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const routeLabel = "networking.waycloak.io/egress-route"

// A CNI ADD can spend at most 5s resolving the Pod, 30s waiting for its
// binding, and 5s in one local programming request. During that transaction
// the durable record intentionally remains LockedDown. Drift reconciliation
// must not race the CNI-owned transition to Ready.
const lockedDownReconcileDelay = 45 * time.Second

// Programmer is the narrow privileged network-namespace boundary. Production
// uses the native nftables/netlink backend; tests use an in-memory recorder.
type Programmer interface {
	Identity(string) (string, error)
	InstallLockdown(context.Context, string, string) error
	Configure(context.Context, string, dataplane.Config) error
	Verify(context.Context, string, dataplane.Config) error
	Cleanup(context.Context, string, string) error
}

type AttachmentStore interface {
	ListAll() ([]waycni.Attachment, error)
	Save(waycni.Attachment) error
	Delete(waycni.Key) error
}

type Observation struct {
	BindingNamespace string    `json:"bindingNamespace"`
	BindingName      string    `json:"bindingName"`
	BindingUID       string    `json:"bindingUID"`
	Generation       int64     `json:"generation"`
	PodUID           string    `json:"podUID"`
	GatewayUID       string    `json:"gatewayUID"`
	NodeName         string    `json:"nodeName"`
	NodeBootID       string    `json:"nodeBootID"`
	InstanceID       string    `json:"instanceID"`
	ObservedAt       time.Time `json:"observedAt"`
	Ready            bool      `json:"ready"`
}

const ReportAPIVersion = "node-observations.waycloak.io/v1"

var (
	ErrPodIdentityInvalid = errors.New("pod identity is invalid")
	ErrPodLookupFailed    = errors.New("kubernetes Pod observation failed")
	ErrPodUIDMismatch     = errors.New("pod UID does not match API observation")
	ErrPodNodeMismatch    = errors.New("pod node does not match local authority")
)

type NodeReport struct {
	NodeName           string                `json:"nodeName"`
	NodeBootID         string                `json:"nodeBootID"`
	InstanceID         string                `json:"instanceID"`
	ObservedAt         time.Time             `json:"observedAt"`
	Ready              bool                  `json:"ready"`
	Capabilities       []string              `json:"capabilities"`
	ReleaseIdentity    wayv1.ReleaseIdentity `json:"releaseIdentity"`
	ConformanceProfile wayv1.QualifiedName   `json:"conformanceProfile"`
}

type Report struct {
	APIVersion   string        `json:"apiVersion"`
	Node         NodeReport    `json:"node"`
	Observations []Observation `json:"observations"`
}

// Service independently resolves caller identities from Kubernetes state and
// owns programming, verification, withdrawal, restart recovery, and drift
// repair for one node.
type Service struct {
	Reader              client.Reader
	Programmer          Programmer
	Store               AttachmentStore
	NodeName            string
	NodeBootID          string
	InstanceID          string
	Now                 func() time.Time
	RequireRelay        bool
	CapabilityHeld      bool
	Capabilities        []string
	ReleaseIdentity     wayv1.ReleaseIdentity
	ConformanceProfile  wayv1.QualifiedName
	OperationErrorHook  func(string, error)
	WithdrawalPublisher func(context.Context, Report) error

	mu             sync.RWMutex
	observations   map[string]Observation
	relayHealthy   atomic.Bool
	backendHealthy atomic.Bool
}

func (s *Service) Resolve(ctx context.Context, identity waycni.PodIdentity) (waycni.Resolution, error) {
	pod, err := s.pod(ctx, identity)
	if err != nil {
		return waycni.Resolution{}, err
	}
	return waycni.Resolution{PodUID: string(pod.UID), Enrolled: pod.Labels[routeLabel] != "", Terminating: !pod.DeletionTimestamp.IsZero()}, nil
}

func (s *Service) Binding(ctx context.Context, identity waycni.PodIdentity) (waycni.Binding, error) {
	pod, err := s.pod(ctx, identity)
	if err != nil {
		return waycni.Binding{}, err
	}
	if pod.Labels[routeLabel] == "" {
		return waycni.Binding{}, waycni.ErrBindingNotReady
	}
	binding, err := s.binding(ctx, pod)
	if err != nil {
		return waycni.Binding{}, err
	}
	return bindingReference(binding), nil
}

func (s *Service) Prepare(ctx context.Context, identity waycni.PodIdentity, requested waycni.Binding) error {
	if !s.Ready() {
		return errors.New("node backend or controller observation relay is unavailable")
	}
	return s.prepare(ctx, identity, requested)
}

func (s *Service) prepare(ctx context.Context, identity waycni.PodIdentity, requested waycni.Binding) error {
	pod, binding, cfg, err := s.authority(ctx, identity, requested)
	if err != nil {
		return err
	}
	if err := s.Programmer.InstallLockdown(ctx, identity.NetNS, identity.UID); err != nil {
		return fmt.Errorf("re-establish deny-first state: %w", err)
	}
	if err := s.Programmer.Configure(ctx, identity.NetNS, cfg); err != nil {
		_ = s.Programmer.InstallLockdown(ctx, identity.NetNS, identity.UID)
		s.observe(binding, false)
		return fmt.Errorf("program protected path with deny retained: %w", err)
	}
	if err := s.Programmer.Verify(ctx, identity.NetNS, cfg); err != nil {
		_ = s.Programmer.InstallLockdown(ctx, identity.NetNS, identity.UID)
		s.observe(binding, false)
		return fmt.Errorf("verify protected path before startup: %w", err)
	}
	_ = pod
	s.observe(binding, true)
	return nil
}

func (s *Service) Check(ctx context.Context, identity waycni.PodIdentity, requested waycni.Binding) error {
	if !s.Ready() {
		_ = s.Programmer.InstallLockdown(ctx, identity.NetNS, identity.UID)
		return errors.New("node backend or controller observation relay is unavailable")
	}
	return s.check(ctx, identity, requested)
}

func (s *Service) check(ctx context.Context, identity waycni.PodIdentity, requested waycni.Binding) error {
	_, binding, cfg, err := s.authority(ctx, identity, requested)
	if err != nil {
		_ = s.Programmer.InstallLockdown(ctx, identity.NetNS, identity.UID)
		return err
	}
	if err := s.Programmer.Verify(ctx, identity.NetNS, cfg); err == nil {
		s.observe(binding, true)
		return nil
	}
	if err := s.Programmer.InstallLockdown(ctx, identity.NetNS, identity.UID); err != nil {
		s.observe(binding, false)
		return fmt.Errorf("restore deny-first state after drift: %w", err)
	}
	if err := s.Programmer.Configure(ctx, identity.NetNS, cfg); err != nil {
		s.observe(binding, false)
		return fmt.Errorf("repair protected path: %w", err)
	}
	if err := s.Programmer.Verify(ctx, identity.NetNS, cfg); err != nil {
		_ = s.Programmer.InstallLockdown(ctx, identity.NetNS, identity.UID)
		s.observe(binding, false)
		return fmt.Errorf("verify repaired protected path: %w", err)
	}
	s.observe(binding, true)
	return nil
}

func (s *Service) SetRelayHealthy(healthy bool) { s.relayHealthy.Store(healthy) }

func (s *Service) SetBackendHealthy(healthy bool) { s.backendHealthy.Store(healthy) }

func (s *Service) Ready() bool {
	return s.backendHealthy.Load() && (!s.RequireRelay || s.relayHealthy.Load())
}

func (s *Service) Status() waycni.AgentStatus {
	return waycni.AgentStatus{NodeName: s.NodeName, NodeBootID: s.NodeBootID, InstanceID: s.InstanceID, Capabilities: append([]string(nil), s.Capabilities...), Ready: s.Ready()}
}

func (s *Service) Report() Report {
	return Report{APIVersion: ReportAPIVersion, Node: NodeReport{
		NodeName: s.NodeName, NodeBootID: s.NodeBootID, InstanceID: s.InstanceID,
		ObservedAt: s.now(), Ready: s.Ready() && !s.CapabilityHeld, Capabilities: append([]string(nil), s.Capabilities...),
		ReleaseIdentity: s.ReleaseIdentity, ConformanceProfile: s.ConformanceProfile,
	}, Observations: s.Observations()}
}

// LockdownAll withdraws every durable attachment without removing exact state.
// It is used when the controller observation relay is unavailable, making
// controller loss a packet-path withdrawal rather than stale permission.
func (s *Service) LockdownAll(ctx context.Context) error {
	attachments, err := s.Store.ListAll()
	if err != nil {
		return err
	}
	var errs []error
	for _, attachment := range attachments {
		namespaceIdentity, identityErr := s.Programmer.Identity(attachment.Pod.NetNS)
		if identityErr != nil && !errors.Is(identityErr, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("observe durable attachment namespace: %w", identityErr))
			continue
		}
		if identityErr != nil || namespaceIdentity != attachment.NamespaceIdentity {
			// The exact Pod may still be pending after a failed ADD. Retain its
			// sticky enrollment record, but never touch a missing or reused netns.
			continue
		}
		if err := s.Programmer.InstallLockdown(ctx, attachment.Pod.NetNS, attachment.Pod.UID); err != nil {
			errs = append(errs, err)
			continue
		}
		if binding, readErr := s.rawBinding(ctx, attachment.Pod.Namespace, attachment.Pod.UID); readErr == nil {
			s.observe(binding, false)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) Withdraw(ctx context.Context, identity waycni.PodIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	attachment, err := s.exactAttachment(identity)
	if err != nil {
		return err
	}
	return s.withdrawAttachment(ctx, attachment)
}

func (s *Service) withdrawAttachment(ctx context.Context, attachment waycni.Attachment) error {
	identity := attachment.Pod
	pod, podErr := s.pod(ctx, identity)
	podAbsent := exactPodAbsent(podErr)
	if podErr != nil && !podAbsent {
		return podErr
	}
	namespaceIdentity, identityErr := s.Programmer.Identity(identity.NetNS)
	if identityErr != nil && !errors.Is(identityErr, fs.ErrNotExist) {
		return fmt.Errorf("observe exact attachment namespace for withdrawal: %w", identityErr)
	}
	exactNamespace := identityErr == nil && namespaceIdentity == attachment.NamespaceIdentity
	if exactNamespace {
		if !podAbsent && pod.DeletionTimestamp.IsZero() {
			if err := s.Programmer.InstallLockdown(ctx, identity.NetNS, identity.UID); err != nil {
				return fmt.Errorf("withdraw allow path while retaining deny: %w", err)
			}
		} else if err := s.Programmer.Cleanup(ctx, identity.NetNS, identity.UID); err != nil {
			return fmt.Errorf("clean terminating exact attachment: %w", err)
		}
	}
	binding, err := s.withdrawalBinding(ctx, identity.Namespace, identity.UID)
	if err != nil {
		return err
	}
	if binding == nil {
		s.forgetObservation(identity.UID)
		return nil
	}
	if s.WithdrawalPublisher == nil {
		return errors.New("synchronous withdrawal publisher is unavailable")
	}
	observation := s.observe(binding, false)
	report := s.Report()
	report.Observations = []Observation{observation}
	if err := s.WithdrawalPublisher(ctx, report); err != nil {
		return fmt.Errorf("publish exact attachment withdrawal: %w", err)
	}
	s.forgetObservation(identity.UID)
	return nil
}

func exactPodAbsent(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, ErrPodUIDMismatch)
}

func (s *Service) forgetObservation(podUID string) {
	s.mu.Lock()
	delete(s.observations, podUID)
	s.mu.Unlock()
}

func (s *Service) exactAttachment(identity waycni.PodIdentity) (waycni.Attachment, error) {
	attachments, err := s.Store.ListAll()
	if err != nil {
		return waycni.Attachment{}, fmt.Errorf("list durable attachments for withdrawal: %w", err)
	}
	for _, attachment := range attachments {
		if attachment.Pod == identity {
			return attachment, nil
		}
	}
	return waycni.Attachment{}, errors.New("exact durable attachment is unavailable")
}

func (s *Service) withdrawalBinding(ctx context.Context, namespace, podUID string) (*wayv1.VPNWorkloadBinding, error) {
	binding := &wayv1.VPNWorkloadBinding{}
	key := client.ObjectKey{Namespace: namespace, Name: waybinding.BindingName(types.UID(podUID))}
	if err := s.Reader.Get(ctx, key, binding); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve exact binding for withdrawal: %w", err)
	}
	if binding.Spec.PodRef.UID != wayv1.ObjectUID(podUID) {
		return nil, errors.New("withdrawal binding Pod UID does not match durable attachment")
	}
	return binding, nil
}

// ReconcileAll rebuilds from durable CNI attachment records after the caller
// has completed a fresh authenticated controller-relay handshake. It bypasses
// the public CNI readiness gate so backend health can recover without depending
// on itself. A revoked or unverifiable live Pod is locked down; only an absent
// exact Pod is cleaned.
func (s *Service) ReconcileAll(ctx context.Context) error {
	attachments, err := s.Store.ListAll()
	if err != nil {
		return fmt.Errorf("list durable CNI attachments: %w", err)
	}
	var errs []error
	for _, attachment := range attachments {
		if attachment.Pod.UID == "" || attachment.Pod.NetNS == "" {
			continue
		}
		pod, podErr := s.pod(ctx, attachment.Pod)
		if exactPodAbsent(podErr) || (podErr == nil && !pod.DeletionTimestamp.IsZero()) {
			if withdrawErr := s.withdrawAttachment(ctx, attachment); withdrawErr != nil {
				errs = append(errs, withdrawErr)
				continue
			}
			if deleteErr := s.Store.Delete(attachment.Key()); deleteErr != nil {
				errs = append(errs, fmt.Errorf("delete withdrawn Pod attachment: %w", deleteErr))
			}
			continue
		}
		if podErr != nil {
			errs = append(errs, podErr)
			continue
		}
		namespaceIdentity, identityErr := s.Programmer.Identity(attachment.Pod.NetNS)
		if identityErr != nil && !errors.Is(identityErr, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("observe durable attachment namespace: %w", identityErr))
			continue
		}
		if identityErr != nil || namespaceIdentity != attachment.NamespaceIdentity {
			// A failed ADD can lose its sandbox before the Pod is deleted. Keep
			// the UID-bound enrollment durable for retries, report not-ready when
			// possible, and never operate on a missing or replacement namespace.
			if binding, readErr := s.rawBinding(ctx, attachment.Pod.Namespace, attachment.Pod.UID); readErr == nil {
				s.observe(binding, false)
			}
			continue
		}
		if attachment.Phase == waycni.PhaseLockedDown {
			age := s.now().Sub(attachment.UpdatedAt)
			if !attachment.UpdatedAt.IsZero() && age >= 0 && age < lockedDownReconcileDelay {
				continue
			}
			errs = append(errs, s.Programmer.InstallLockdown(ctx, attachment.Pod.NetNS, attachment.Pod.UID))
			continue
		}
		binding, readErr := s.binding(ctx, pod)
		if readErr != nil || string(binding.UID) != attachment.BindingUID || string(binding.Spec.GatewayRef.UID) != attachment.GatewayUID {
			lockErr := s.Programmer.InstallLockdown(ctx, attachment.Pod.NetNS, attachment.Pod.UID)
			errs = append(errs, errors.Join(errors.New("durable attachment binding was revoked or replaced"), lockErr))
			if readErr == nil {
				s.observe(binding, false)
			}
			continue
		}
		requested := bindingReference(binding)
		var reconcileErr error
		if attachment.BindingGeneration != requested.Generation {
			reconcileErr = s.prepare(ctx, attachment.Pod, requested)
		} else {
			reconcileErr = s.check(ctx, attachment.Pod, requested)
		}
		if reconcileErr != nil {
			if binding, readErr := s.rawBinding(ctx, attachment.Pod.Namespace, attachment.Pod.UID); readErr == nil {
				s.observe(binding, false)
			}
			errs = append(errs, reconcileErr)
			continue
		}
		if attachment.BindingGeneration != requested.Generation {
			attachment.BindingGeneration = requested.Generation
			attachment.UpdatedAt = s.now()
			if err := s.Store.Save(attachment); err != nil {
				_ = s.Programmer.InstallLockdown(ctx, attachment.Pod.NetNS, attachment.Pod.UID)
				errs = append(errs, fmt.Errorf("persist adopted binding generation: %w", err))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Service) Observations() []Observation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Observation, 0, len(s.observations))
	for _, observation := range s.observations {
		result = append(result, observation)
	}
	return result
}

func (s *Service) authority(ctx context.Context, identity waycni.PodIdentity, requested waycni.Binding) (*corev1.Pod, *wayv1.VPNWorkloadBinding, dataplane.Config, error) {
	if err := requested.Validate(identity.UID); err != nil {
		return nil, nil, dataplane.Config{}, err
	}
	pod, err := s.pod(ctx, identity)
	if err != nil {
		return nil, nil, dataplane.Config{}, err
	}
	binding, err := s.binding(ctx, pod)
	if err != nil {
		return nil, nil, dataplane.Config{}, err
	}
	if actual := bindingReference(binding); actual != requested {
		return nil, nil, dataplane.Config{}, errors.New("requested binding identity or generation is stale")
	}
	cfg, err := ConfigFromBinding(binding)
	if err != nil {
		return nil, nil, dataplane.Config{}, err
	}
	return pod, binding, cfg, nil
}

func (s *Service) pod(ctx context.Context, identity waycni.PodIdentity) (*corev1.Pod, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPodIdentityInvalid, err)
	}
	if s.Reader == nil || s.Programmer == nil || s.NodeName == "" {
		return nil, fmt.Errorf("%w: node agent dependencies are incomplete", ErrPodIdentityInvalid)
	}
	pod := &corev1.Pod{}
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: identity.Namespace, Name: identity.Name}, pod); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPodLookupFailed, err)
	}
	if string(pod.UID) != identity.UID {
		return nil, ErrPodUIDMismatch
	}
	if pod.Spec.NodeName != s.NodeName {
		return nil, ErrPodNodeMismatch
	}
	return pod, nil
}

func (s *Service) binding(ctx context.Context, pod *corev1.Pod) (*wayv1.VPNWorkloadBinding, error) {
	binding, err := s.rawBinding(ctx, pod.Namespace, string(pod.UID))
	if err != nil {
		return nil, err
	}
	if !binding.DeletionTimestamp.IsZero() || binding.Spec.PodRef.Name != wayv1.ObjectName(pod.Name) || binding.Spec.NodeName != wayv1.ObjectName(s.NodeName) || binding.UID == "" {
		return nil, waycni.ErrBindingNotReady
	}
	return binding, nil
}

func (s *Service) rawBinding(ctx context.Context, namespace, podUID string) (*wayv1.VPNWorkloadBinding, error) {
	binding := &wayv1.VPNWorkloadBinding{}
	key := client.ObjectKey{Namespace: namespace, Name: waybinding.BindingName(types.UID(podUID))}
	if err := s.Reader.Get(ctx, key, binding); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, waycni.ErrBindingNotReady
		}
		return nil, err
	}
	if binding.Spec.PodRef.UID != wayv1.ObjectUID(podUID) {
		return nil, waycni.ErrBindingNotReady
	}
	return binding, nil
}

func bindingReference(binding *wayv1.VPNWorkloadBinding) waycni.Binding {
	return waycni.Binding{UID: string(binding.UID), Generation: binding.Generation, PodUID: string(binding.Spec.PodRef.UID), GatewayUID: string(binding.Spec.GatewayRef.UID)}
}

func ConfigFromBinding(binding *wayv1.VPNWorkloadBinding) (dataplane.Config, error) {
	if binding == nil || binding.Generation < 1 {
		return dataplane.Config{}, errors.New("current binding is required")
	}
	overlay, err := netip.ParsePrefix(binding.Spec.Network.OverlayCIDR)
	if err != nil {
		return dataplane.Config{}, fmt.Errorf("parse projected overlay CIDR: %w", err)
	}
	allocated, err := netip.ParsePrefix(binding.Spec.Allocation.Address)
	if err != nil {
		return dataplane.Config{}, fmt.Errorf("parse allocated address: %w", err)
	}
	gateway, err := netip.ParseAddr(binding.Spec.Network.GatewayAddress)
	if err != nil {
		return dataplane.Config{}, fmt.Errorf("parse projected gateway address: %w", err)
	}
	endpoint, err := netip.ParseAddrPort(binding.Spec.Network.GatewayEndpoint)
	if err != nil {
		return dataplane.Config{}, fmt.Errorf("parse projected gateway endpoint: %w", err)
	}
	mode := dataplane.ClusterTrafficGateway
	if binding.Spec.Network.ClusterTraffic.Mode == wayv1.ClusterTrafficBypassCluster {
		mode = dataplane.ClusterTrafficPreserve
	}
	config := dataplane.Config{
		PodUID: string(binding.Spec.PodRef.UID), AllocationGeneration: binding.Generation,
		GatewayGeneration: binding.Spec.Network.GatewayGeneration, Address: netip.PrefixFrom(allocated.Addr(), overlay.Bits()),
		OverlayCIDR: overlay, GatewayAddress: gateway, GatewayEndpoint: endpoint,
		GatewayHealthPort: uint16(binding.Spec.Network.GatewayHealthPort), VNI: uint32(binding.Spec.Network.VNI),
		MTU: int(binding.Spec.Network.MTU), ClusterTrafficMode: mode,
	}
	for _, text := range binding.Spec.Network.ClusterTraffic.BypassCIDRs {
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			return dataplane.Config{}, fmt.Errorf("parse projected bypass CIDR: %w", err)
		}
		config.ClusterCIDRs = append(config.ClusterCIDRs, prefix)
	}
	return config, config.Validate()
}

func (s *Service) observe(binding *wayv1.VPNWorkloadBinding, ready bool) Observation {
	observation := Observation{
		BindingNamespace: binding.Namespace, BindingName: binding.Name, BindingUID: string(binding.UID),
		Generation: binding.Generation, PodUID: string(binding.Spec.PodRef.UID), GatewayUID: string(binding.Spec.GatewayRef.UID),
		NodeName: s.NodeName, NodeBootID: s.NodeBootID, InstanceID: s.InstanceID, ObservedAt: s.now(), Ready: ready,
	}
	s.mu.Lock()
	if s.observations == nil {
		s.observations = make(map[string]Observation)
	}
	s.observations[observation.PodUID] = observation
	s.mu.Unlock()
	return observation
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
