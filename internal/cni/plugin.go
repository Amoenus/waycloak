// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

type Plugin struct {
	Agent          Agent
	Enforcer       Enforcer
	Store          Store
	ResolveTimeout time.Duration
	BindingTimeout time.Duration
	RetryInterval  time.Duration
	Now            func() time.Time
}

func (p Plugin) Add(ctx context.Context, request Request) error {
	if err := p.validate(request); err != nil {
		return err
	}
	namespaceIdentity, err := p.Enforcer.Identity(request.Pod.NetNS)
	if err != nil {
		return fmt.Errorf("resolve exact network namespace identity: %w", err)
	}
	existing, loadErr := p.Store.Load(request.Key())
	if loadErr == nil {
		if err := sameAttachment(existing, request, namespaceIdentity); err != nil {
			return err
		}
		if existing.Phase == PhaseReady && existing.Config != nil {
			if err := p.Agent.Check(ctx, request.Pod); err == nil {
				if err := p.Enforcer.Verify(ctx, request.Pod.NetNS, *existing.Config); err == nil {
					return nil
				}
			}
		}
	} else {
		if !errors.Is(loadErr, fs.ErrNotExist) {
			return fmt.Errorf("load prior attachment state: %w", loadErr)
		}
		sticky, err := p.hasPriorEnrollment(request)
		if err != nil {
			return fmt.Errorf("find prior UID-bound enrollment: %w", err)
		}
		if !sticky {
			resolution, err := retryValue(ctx, p.resolveTimeout(), p.retryInterval(), func(attempt context.Context) (Resolution, error) {
				return p.Agent.Resolve(attempt, request.Pod)
			})
			if err != nil {
				return fmt.Errorf("resolve exact Pod enrollment through local agent: %w", err)
			}
			if resolution.PodUID != request.Pod.UID {
				return fmt.Errorf("local agent resolved Pod UID %q, expected %q", resolution.PodUID, request.Pod.UID)
			}
			if !resolution.Enrolled {
				return nil
			}
		}
	}

	if err := p.Enforcer.InstallLockdown(ctx, request.Pod.NetNS, request.Pod.UID); err != nil {
		return fmt.Errorf("install deny-first policy: %w", err)
	}
	attachment := Attachment{
		Network: request.Network, Pod: request.Pod, NamespaceIdentity: namespaceIdentity,
		Phase: PhaseLockedDown, UpdatedAt: p.now(),
	}
	if err := p.Store.Save(attachment); err != nil {
		rollbackErr := p.Enforcer.Cleanup(ctx, request.Pod.NetNS, request.Pod.UID)
		return errors.Join(fmt.Errorf("record deny-first attachment state: %w", err), errorWithContext("roll back unrecorded deny-first state", rollbackErr))
	}

	binding, err := retryValue(ctx, p.bindingTimeout(), p.retryInterval(), func(attempt context.Context) (Binding, error) {
		return p.Agent.Binding(attempt, request.Pod)
	})
	if err != nil {
		return fmt.Errorf("wait for UID-bound allocation with deny retained: %w", err)
	}
	if binding.PodUID != request.Pod.UID || binding.Config.PodUID != request.Pod.UID {
		return fmt.Errorf("binding Pod UID does not match exact CNI Pod UID %q", request.Pod.UID)
	}
	if err := binding.Config.Validate(); err != nil {
		return fmt.Errorf("validate UID-bound protected-path configuration with deny retained: %w", err)
	}
	attachment.Config = &binding.Config
	attachment.UpdatedAt = p.now()
	if err := p.Store.Save(attachment); err != nil {
		return fmt.Errorf("record allocated attachment state: %w", err)
	}
	if err := p.Enforcer.Configure(ctx, request.Pod.NetNS, binding.Config); err != nil {
		return fmt.Errorf("program protected path after deny: %w", err)
	}
	if err := p.Enforcer.Verify(ctx, request.Pod.NetNS, binding.Config); err != nil {
		return fmt.Errorf("verify protected path before CNI success: %w", err)
	}
	attachment.Phase = PhaseReady
	attachment.UpdatedAt = p.now()
	if err := p.Store.Save(attachment); err != nil {
		return fmt.Errorf("record ready attachment state: %w", err)
	}
	return nil
}

func errorWithContext(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func (p Plugin) Check(ctx context.Context, request Request) error {
	if err := p.validate(request); err != nil {
		return err
	}
	attachment, err := p.Store.Load(request.Key())
	if err != nil {
		return fmt.Errorf("load attachment state for CHECK: %w", err)
	}
	namespaceIdentity, err := p.Enforcer.Identity(request.Pod.NetNS)
	if err != nil {
		return fmt.Errorf("resolve network namespace identity for CHECK: %w", err)
	}
	if err := sameAttachment(attachment, request, namespaceIdentity); err != nil {
		return err
	}
	if attachment.Phase != PhaseReady || attachment.Config == nil {
		return errors.New("attachment has not completed deny-first protected-path setup")
	}
	if err := p.Agent.Check(ctx, request.Pod); err != nil {
		return fmt.Errorf("local agent cannot confirm live attachment: %w", err)
	}
	if err := p.Enforcer.Verify(ctx, request.Pod.NetNS, *attachment.Config); err != nil {
		return fmt.Errorf("verify live protected path: %w", err)
	}
	return nil
}

func (p Plugin) Delete(ctx context.Context, key Key, netns string) error {
	if p.Agent == nil || p.Store == nil || p.Enforcer == nil {
		return errors.New("local agent, CNI store, and enforcer are required")
	}
	attachment, err := p.Store.Load(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load attachment state for DEL: %w", err)
	}
	// A runtime may issue DEL after a failed chained ADD and then retry the
	// same Pod with a new sandbox identity. Retain the UID-bound enrollment
	// record while that exact Pod still exists so removing its label cannot
	// turn the next ADD into an ordinary-egress success. Unreachable agents
	// are treated the same way; GC removes the stale record later.
	resolution, resolveErr := p.Agent.Resolve(ctx, attachment.Pod)
	if resolveErr != nil || resolution.PodUID != attachment.Pod.UID || !resolution.Terminating {
		return nil
	}
	path := netns
	if path == "" {
		path = attachment.Pod.NetNS
	}
	identity, identityErr := p.Enforcer.Identity(path)
	if identityErr == nil && identity == attachment.NamespaceIdentity {
		if err := p.Enforcer.Cleanup(ctx, path, attachment.Pod.UID); err != nil {
			return fmt.Errorf("remove exact Waycloak network state: %w", err)
		}
	}
	_ = p.Agent.Withdraw(ctx, attachment.Pod)
	if err := p.Store.Delete(key); err != nil {
		return fmt.Errorf("delete attachment state: %w", err)
	}
	return nil
}

func (p Plugin) hasPriorEnrollment(request Request) (bool, error) {
	attachments, err := p.Store.List(request.Network)
	if err != nil {
		return false, err
	}
	for _, attachment := range attachments {
		if attachment.Pod.Namespace == request.Pod.Namespace && attachment.Pod.Name == request.Pod.Name && attachment.Pod.UID == request.Pod.UID {
			return true, nil
		}
	}
	return false, nil
}

func (p Plugin) GC(ctx context.Context, network string, valid map[Key]struct{}) error {
	if p.Store == nil || p.Enforcer == nil {
		return errors.New("CNI store and enforcer are required")
	}
	attachments, err := p.Store.List(network)
	if err != nil {
		return err
	}
	var errs []error
	for _, attachment := range attachments {
		if _, ok := valid[attachment.Key()]; ok {
			continue
		}
		identity, identityErr := p.Enforcer.Identity(attachment.Pod.NetNS)
		if identityErr == nil && identity == attachment.NamespaceIdentity {
			if err := p.Enforcer.Cleanup(ctx, attachment.Pod.NetNS, attachment.Pod.UID); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := p.Store.Delete(attachment.Key()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p Plugin) Status(ctx context.Context) error {
	if p.Agent == nil {
		return errors.New("local agent is required")
	}
	return p.Agent.Status(ctx)
}

func (p Plugin) validate(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if p.Agent == nil || p.Enforcer == nil || p.Store == nil {
		return errors.New("local agent, namespace enforcer, and state store are required")
	}
	return nil
}

func sameAttachment(attachment Attachment, request Request, namespaceIdentity string) error {
	if attachment.Network != request.Network || attachment.Pod != request.Pod {
		return errors.New("stored attachment does not match exact CNI attachment identity")
	}
	if attachment.NamespaceIdentity != namespaceIdentity {
		return errors.New("network namespace identity changed for the same CNI attachment")
	}
	return nil
}

func retryValue[T any](parent context.Context, timeout, interval time.Duration, operation func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for {
		value, err := operation(ctx)
		if err == nil {
			return value, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, fmt.Errorf("bounded wait expired after %s: %w", timeout, err)
		case <-timer.C:
		}
	}
}

func (p Plugin) resolveTimeout() time.Duration {
	if p.ResolveTimeout > 0 {
		return p.ResolveTimeout
	}
	return 2 * time.Second
}

func (p Plugin) bindingTimeout() time.Duration {
	if p.BindingTimeout > 0 {
		return p.BindingTimeout
	}
	return 10 * time.Second
}

func (p Plugin) retryInterval() time.Duration {
	if p.RetryInterval > 0 {
		return p.RetryInterval
	}
	return 100 * time.Millisecond
}

func (p Plugin) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
