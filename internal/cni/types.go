// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const AgentAPIVersion = "networking.waycloak.io/cni-node/v1"

var (
	ErrBindingNotReady = errors.New("workload binding is not ready")
	ErrPodNotFound     = errors.New("exact Kubernetes Pod is absent")
)

type PodIdentity struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	UID         string `json:"uid"`
	ContainerID string `json:"containerID"`
	IfName      string `json:"ifName"`
	NetNS       string `json:"netNS"`
}

func (p PodIdentity) Validate() error {
	if p.Namespace == "" || p.Name == "" || p.UID == "" {
		return errors.New("exact Pod namespace, name, and UID are required")
	}
	if p.ContainerID == "" || p.IfName == "" || p.NetNS == "" {
		return errors.New("exact sandbox container, interface, and network namespace identities are required")
	}
	return nil
}

type Resolution struct {
	PodUID      string `json:"podUID"`
	Enrolled    bool   `json:"enrolled"`
	Terminating bool   `json:"terminating,omitempty"`
}

type Binding struct {
	UID        string `json:"uid"`
	Generation int64  `json:"generation"`
	PodUID     string `json:"podUID"`
	GatewayUID string `json:"gatewayUID"`
}

func (b Binding) Validate(expectedPodUID string) error {
	if b.UID == "" || b.Generation < 1 || b.PodUID == "" || b.GatewayUID == "" {
		return errors.New("binding UID, generation, Pod UID, and gateway UID are required")
	}
	if b.PodUID != expectedPodUID {
		return errors.New("binding Pod UID does not match exact CNI Pod UID")
	}
	return nil
}

type Agent interface {
	Resolve(context.Context, PodIdentity) (Resolution, error)
	Binding(context.Context, PodIdentity) (Binding, error)
	Prepare(context.Context, PodIdentity, Binding) error
	Check(context.Context, PodIdentity, Binding) error
	Withdraw(context.Context, PodIdentity) error
	Status(context.Context) error
}

type Enforcer interface {
	Identity(string) (string, error)
	InstallLockdown(context.Context, string, string) error
	Cleanup(context.Context, string, string) error
}

type Phase string

const (
	PhaseLockedDown Phase = "LockedDown"
	PhaseReady      Phase = "Ready"
)

type Attachment struct {
	Network           string      `json:"network"`
	Pod               PodIdentity `json:"pod"`
	NamespaceIdentity string      `json:"namespaceIdentity"`
	Phase             Phase       `json:"phase"`
	BindingUID        string      `json:"bindingUID,omitempty"`
	BindingGeneration int64       `json:"bindingGeneration,omitempty"`
	GatewayUID        string      `json:"gatewayUID,omitempty"`
	UpdatedAt         time.Time   `json:"updatedAt"`
}

func (a Attachment) Key() Key {
	return Key{Network: a.Network, ContainerID: a.Pod.ContainerID, IfName: a.Pod.IfName}
}

func (a Attachment) Validate() error {
	if a.Network == "" {
		return errors.New("CNI network name is required")
	}
	if err := a.Pod.Validate(); err != nil {
		return err
	}
	if a.NamespaceIdentity == "" {
		return errors.New("network namespace identity is required")
	}
	switch a.Phase {
	case PhaseLockedDown:
	case PhaseReady:
		if a.BindingUID == "" || a.BindingGeneration < 1 || a.GatewayUID == "" {
			return errors.New("ready attachment requires exact current binding identity")
		}
	default:
		return fmt.Errorf("unknown attachment phase %q", a.Phase)
	}
	return nil
}

type Key struct {
	Network     string
	ContainerID string
	IfName      string
}

func (k Key) Validate() error {
	if k.Network == "" || k.ContainerID == "" || k.IfName == "" {
		return errors.New("network, container ID, and interface name are required")
	}
	return nil
}

type Store interface {
	Load(Key) (Attachment, error)
	Save(Attachment) error
	Delete(Key) error
	List(string) ([]Attachment, error)
}

type Request struct {
	Network string
	Pod     PodIdentity
}

func (r Request) Key() Key {
	return Key{Network: r.Network, ContainerID: r.Pod.ContainerID, IfName: r.Pod.IfName}
}

func (r Request) Validate() error {
	if r.Network == "" {
		return errors.New("CNI network name is required")
	}
	return r.Pod.Validate()
}
