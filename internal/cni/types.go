// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Amoenus/waycloak/internal/dataplane"
)

const AgentAPIVersion = "networking.waycloak.io/cni-feasibility/v1"

var ErrBindingNotReady = errors.New("workload binding is not ready")

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
	PodUID string           `json:"podUID"`
	Config dataplane.Config `json:"config"`
}

type Agent interface {
	Resolve(context.Context, PodIdentity) (Resolution, error)
	Binding(context.Context, PodIdentity) (Binding, error)
	Check(context.Context, PodIdentity) error
	Withdraw(context.Context, PodIdentity) error
	Status(context.Context) error
}

type Enforcer interface {
	Identity(string) (string, error)
	InstallLockdown(context.Context, string, string) error
	Configure(context.Context, string, dataplane.Config) error
	Verify(context.Context, string, dataplane.Config) error
	Cleanup(context.Context, string, string) error
}

type Phase string

const (
	PhaseLockedDown Phase = "LockedDown"
	PhaseReady      Phase = "Ready"
)

type Attachment struct {
	Network           string            `json:"network"`
	Pod               PodIdentity       `json:"pod"`
	NamespaceIdentity string            `json:"namespaceIdentity"`
	Phase             Phase             `json:"phase"`
	Config            *dataplane.Config `json:"config,omitempty"`
	UpdatedAt         time.Time         `json:"updatedAt"`
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
		if a.Config == nil {
			return errors.New("ready attachment requires protected-path configuration")
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
