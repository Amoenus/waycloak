// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package scheduling

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/nodeagent"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	coreapply "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	CoreReadyLabel       = "networking.waycloak.io.node-restriction.kubernetes.io/core-ready"
	CapabilityEpochLabel = "networking.waycloak.io.node-restriction.kubernetes.io/capability-epoch"
	FieldManager         = "waycloak-node-capability-controller"
	DefaultFreshness     = 20 * time.Second
	DefaultClockSkew     = time.Minute
)

var CoreCapabilities = []string{"dns-udp-tcp", "ipv4", "netlink", "nftables", "vxlan"}

type Publisher struct {
	Client             client.Client
	ReleaseIdentity    wayv1.ReleaseIdentity
	ConformanceProfile wayv1.QualifiedName
	Required           []string
	Now                func() time.Time
	ClockSkew          time.Duration
}

func (p Publisher) Apply(ctx context.Context, agentPod *corev1.Pod, report nodeagent.NodeReport) error {
	if p.Client == nil || agentPod == nil || agentPod.Spec.NodeName == "" {
		return errors.New("node capability publisher and exact agent Pod are required")
	}
	if report.NodeName != agentPod.Spec.NodeName || report.NodeBootID == "" || report.InstanceID == "" {
		return errors.New("node capability report does not match the authenticated agent Pod")
	}
	now := p.now()
	skew := p.clockSkew()
	if report.ObservedAt.IsZero() || report.ObservedAt.Before(now.Add(-skew)) || report.ObservedAt.After(now.Add(skew)) {
		_ = p.withdraw(ctx, report.NodeName)
		return errors.New("node capability observation time is outside the accepted window")
	}
	if report.ReleaseIdentity != p.ReleaseIdentity || report.ConformanceProfile != p.ConformanceProfile || !containsAll(report.Capabilities, p.required()) {
		_ = p.withdraw(ctx, report.NodeName)
		return errors.New("node capability or immutable release identity is unsupported")
	}
	if !report.Ready {
		return p.withdraw(ctx, report.NodeName)
	}
	if err := p.Client.Get(ctx, client.ObjectKey{Name: report.NodeName}, &corev1.Node{}); err != nil {
		return err
	}
	owned := coreapply.Node(report.NodeName).WithLabels(map[string]string{
		CoreReadyLabel: "true", CapabilityEpochLabel: strconv.FormatInt(now.Unix(), 10),
	})
	if err := p.Client.Apply(ctx, owned, client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
		return fmt.Errorf("publish authenticated node capability: %w", err)
	}
	return nil
}

func (p Publisher) Withdraw(ctx context.Context, nodeName string) error {
	return p.withdraw(ctx, nodeName)
}

func (p Publisher) withdraw(ctx context.Context, nodeName string) error {
	if p.Client == nil || nodeName == "" {
		return errors.New("node capability publisher and node name are required")
	}
	node := &corev1.Node{}
	if err := p.Client.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return client.IgnoreNotFound(err)
	}
	if node.Labels[CoreReadyLabel] == "" && node.Labels[CapabilityEpochLabel] == "" {
		return nil
	}
	before := node.DeepCopy()
	delete(node.Labels, CoreReadyLabel)
	delete(node.Labels, CapabilityEpochLabel)
	if err := p.Client.Patch(ctx, node, client.MergeFrom(before), client.FieldOwner(FieldManager)); err != nil {
		return fmt.Errorf("withdraw authenticated node capability: %w", err)
	}
	return nil
}

func (p Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p Publisher) clockSkew() time.Duration {
	if p.ClockSkew > 0 {
		return p.ClockSkew
	}
	return DefaultClockSkew
}

func (p Publisher) required() []string {
	if len(p.Required) != 0 {
		return p.Required
	}
	return CoreCapabilities
}

func containsAll(actual, required []string) bool {
	for _, capability := range required {
		if !slices.Contains(actual, capability) {
			return false
		}
	}
	return true
}

func Stale(node *corev1.Node, now time.Time, freshness time.Duration) bool {
	if node == nil || node.Labels[CoreReadyLabel] != "true" {
		return false
	}
	epoch, err := strconv.ParseInt(node.Labels[CapabilityEpochLabel], 10, 64)
	if err != nil {
		return true
	}
	observed := time.Unix(epoch, 0).UTC()
	return observed.After(now.Add(DefaultClockSkew)) || now.Sub(observed) > freshness
}

func IgnoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
