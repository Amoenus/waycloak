// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeAgent struct {
	mu            sync.Mutex
	events        *[]string
	resolution    Resolution
	binding       Binding
	prepareWant   Binding
	bindingErrors []error
	resolveError  error
	prepareError  error
	checkError    error
	withdrawError error
	statusError   error
}

func (a *fakeAgent) record(event string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	*a.events = append(*a.events, event)
}

func (a *fakeAgent) Resolve(context.Context, PodIdentity) (Resolution, error) {
	a.record("resolve")
	return a.resolution, a.resolveError
}

func (a *fakeAgent) Binding(context.Context, PodIdentity) (Binding, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	*a.events = append(*a.events, "binding")
	if len(a.bindingErrors) > 0 {
		err := a.bindingErrors[0]
		a.bindingErrors = a.bindingErrors[1:]
		return Binding{}, err
	}
	return a.binding, nil
}

func (a *fakeAgent) Prepare(_ context.Context, _ PodIdentity, binding Binding) error {
	a.record("prepare")
	if binding != a.prepareWant {
		return errors.New("stale binding")
	}
	a.record("configure")
	if a.prepareError != nil {
		return a.prepareError
	}
	a.record("verify")
	return nil
}

func (a *fakeAgent) Check(context.Context, PodIdentity, Binding) error {
	a.record("agent-check")
	return a.checkError
}

func (a *fakeAgent) Withdraw(context.Context, PodIdentity) error {
	a.record("withdraw")
	return a.withdrawError
}

func (a *fakeAgent) Status(context.Context) error { return a.statusError }

type fakeEnforcer struct {
	events         *[]string
	identities     map[string]string
	identityErrors map[string]error
	lockdownError  error
	cleanupError   error
}

type failingSaveStore struct {
	Store
	err error
}

func (s failingSaveStore) Save(Attachment) error { return s.err }

func (e *fakeEnforcer) record(event string) { *e.events = append(*e.events, event) }

func (e *fakeEnforcer) Identity(path string) (string, error) {
	e.record("identity")
	if err := e.identityErrors[path]; err != nil {
		return "", err
	}
	return e.identities[path], nil
}

func (e *fakeEnforcer) InstallLockdown(context.Context, string, string) error {
	e.record("lockdown")
	return e.lockdownError
}

func (e *fakeEnforcer) Cleanup(_ context.Context, _ string, _ string) error {
	e.record("cleanup")
	return e.cleanupError
}

func TestAddInstallsDenyBeforeBindingAndProtectedPath(t *testing.T) {
	plugin, request, events := fixture(t)
	agent := plugin.Agent.(*fakeAgent)
	agent.bindingErrors = []error{ErrBindingNotReady, errors.New("agent restarting")}
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := []string{"identity", "resolve", "lockdown", "binding", "binding", "binding", "prepare", "configure", "verify"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("operation order = %v, want %v", *events, want)
	}
	attachment, err := plugin.Store.Load(request.Key())
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Phase != PhaseReady || attachment.BindingUID == "" || attachment.GatewayUID == "" {
		t.Fatalf("ready attachment = %#v", attachment)
	}
}

func TestAddFailureAfterPrimaryCNIRetainsDenyForDEL(t *testing.T) {
	plugin, request, events := fixture(t)
	plugin.Agent.(*fakeAgent).prepareError = errors.New("injected programming failure")
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("programming failure unexpectedly succeeded")
	}
	want := []string{"identity", "resolve", "lockdown", "binding", "prepare", "configure"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("operation order = %v, want %v", *events, want)
	}
	attachment, err := plugin.Store.Load(request.Key())
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Phase != PhaseLockedDown || attachment.BindingUID == "" {
		t.Fatalf("partial attachment did not retain deny state: %#v", attachment)
	}
	plugin.Agent.(*fakeAgent).resolution.Terminating = true
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Store.Load(request.Key()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state remained after DEL: %v", err)
	}
	if !reflect.DeepEqual((*events)[len(*events)-4:], []string{"resolve", "identity", "withdraw", "cleanup"}) {
		t.Fatalf("DEL order = %v", (*events)[len(*events)-4:])
	}
}

func TestAddRollsBackDenyWhenInitialStateCannotBeRecorded(t *testing.T) {
	plugin, request, events := fixture(t)
	plugin.Store = failingSaveStore{Store: plugin.Store, err: errors.New("injected durable-state failure")}
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("state persistence failure unexpectedly succeeded")
	}
	want := []string{"identity", "resolve", "lockdown", "cleanup"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("rollback operations = %v, want %v", *events, want)
	}
}

func TestAddBindingWaitIsBoundedWithDenyRetained(t *testing.T) {
	plugin, request, events := fixture(t)
	agent := plugin.Agent.(*fakeAgent)
	agent.bindingErrors = []error{ErrBindingNotReady, ErrBindingNotReady, ErrBindingNotReady, ErrBindingNotReady, ErrBindingNotReady}
	plugin.BindingTimeout = 8 * time.Millisecond
	plugin.RetryInterval = 2 * time.Millisecond
	started := time.Now()
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("missing binding unexpectedly succeeded")
	}
	// The race detector can add substantial filesystem and scheduler latency on
	// shared CI runners. Keep the wall-clock ceiling finite without coupling the
	// assertion to a small multiple of the configured binding timeout.
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("binding wait was not bounded: %s", elapsed)
	}
	if _, err := plugin.Store.Load(request.Key()); err != nil {
		t.Fatalf("deny state was not retained: %v", err)
	}
	if index(*events, "lockdown") > index(*events, "binding") {
		t.Fatalf("binding was checked before lockdown: %v", *events)
	}
}

func TestAddRejectsMissingStaleOrMismatchedBindingIdentityWithDenyRetained(t *testing.T) {
	tests := map[string]func(*Binding){
		"missing binding UID":      func(binding *Binding) { binding.UID = "" },
		"stale generation":         func(binding *Binding) { binding.Generation++ },
		"gateway identity missing": func(binding *Binding) { binding.GatewayUID = "" },
		"Pod UID mismatch":         func(binding *Binding) { binding.PodUID = "replacement-uid" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			plugin, request, events := fixture(t)
			mutate(&plugin.Agent.(*fakeAgent).binding)
			if err := plugin.Add(context.Background(), request); err == nil {
				t.Fatal("invalid binding unexpectedly allowed CNI ADD")
			}
			attachment, err := plugin.Store.Load(request.Key())
			if err != nil || attachment.Phase != PhaseLockedDown {
				t.Fatalf("deny-first state = %#v, %v", attachment, err)
			}
			if index(*events, "configure") >= 0 || index(*events, "verify") >= 0 {
				t.Fatalf("invalid binding reached programming: %v", *events)
			}
		})
	}
}

func TestAddRejectsUIDAndNamespaceReuse(t *testing.T) {
	plugin, request, _ := fixture(t)
	agent := plugin.Agent.(*fakeAgent)
	agent.resolution.PodUID = "replacement-uid"
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("wrong resolved Pod UID was accepted")
	}
	agent.resolution.PodUID = request.Pod.UID
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	plugin.Enforcer.(*fakeEnforcer).identities[request.Pod.NetNS] = "22:44"
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("reused network namespace path was accepted")
	}
}

func TestDuplicateAddAndDeleteAreIdempotent(t *testing.T) {
	plugin, request, events := fixture(t)
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	before := len(*events)
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := (*events)[before:]; !reflect.DeepEqual(got, []string{"identity", "agent-check"}) {
		t.Fatalf("duplicate ADD operations = %v", got)
	}
	plugin.Agent.(*fakeAgent).resolution.Terminating = true
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err != nil {
		t.Fatalf("duplicate DEL failed: %v", err)
	}
}

func TestDurableEnrollmentCannotBeRemovedBetweenAddRetries(t *testing.T) {
	plugin, request, events := fixture(t)
	plugin.Agent.(*fakeAgent).prepareError = errors.New("injected programming failure")
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("initial ADD failure unexpectedly succeeded")
	}
	plugin.Agent.(*fakeAgent).prepareError = nil
	plugin.Agent.(*fakeAgent).resolution.Enrolled = false
	*events = nil
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if index(*events, "resolve") >= 0 || index(*events, "binding") < 0 || index(*events, "configure") < 0 || index(*events, "verify") < 0 {
		t.Fatalf("durable enrollment was not retained across ADD retry: %v", *events)
	}
}

func TestDurableEnrollmentSurvivesRuntimeDeleteAndNewSandbox(t *testing.T) {
	plugin, request, events := fixture(t)
	plugin.Agent.(*fakeAgent).prepareError = errors.New("programming unavailable")
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("initial ADD failure unexpectedly succeeded")
	}
	plugin.Agent.(*fakeAgent).resolution.Enrolled = false
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Store.Load(request.Key()); err != nil {
		t.Fatalf("live Pod enrollment record was removed by runtime DEL: %v", err)
	}

	retry := request
	retry.Pod.ContainerID = "replacement-sandbox"
	retry.Pod.NetNS = "/netns/replacement"
	plugin.Enforcer.(*fakeEnforcer).identities[retry.Pod.NetNS] = "12:35"
	plugin.Agent.(*fakeAgent).prepareError = nil
	*events = nil
	if err := plugin.Add(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	if index(*events, "resolve") >= 0 || index(*events, "lockdown") < 0 || index(*events, "binding") < 0 {
		t.Fatalf("UID-bound enrollment was not retained across sandbox replacement: %v", *events)
	}
}

func TestDeleteAbsentPodReportsWithdrawalBeforeDiscardingState(t *testing.T) {
	plugin, request, events := fixture(t)
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	agent := plugin.Agent.(*fakeAgent)
	agent.resolveError = ErrPodNotFound
	plugin.Enforcer.(*fakeEnforcer).identityErrors[request.Pod.NetNS] = fs.ErrNotExist
	*events = nil
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Store.Load(request.Key()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent Pod attachment state remains: %v", err)
	}
	if want := []string{"resolve", "identity", "withdraw"}; !reflect.DeepEqual(*events, want) {
		t.Fatalf("absent Pod DEL operations = %v, want %v", *events, want)
	}
}

func TestDeleteRetainsDurableStateUntilWithdrawalIsObserved(t *testing.T) {
	plugin, request, _ := fixture(t)
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	agent := plugin.Agent.(*fakeAgent)
	agent.resolveError = ErrPodNotFound
	agent.withdrawError = errors.New("node agent unavailable")
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err == nil {
		t.Fatal("DEL discarded state without a node-agent withdrawal observation")
	}
	if _, err := plugin.Store.Load(request.Key()); err != nil {
		t.Fatalf("failed withdrawal discarded durable state: %v", err)
	}
}

func TestDeleteReturnsAmbiguousResolutionFailureWithStateRetained(t *testing.T) {
	plugin, request, _ := fixture(t)
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	plugin.Agent.(*fakeAgent).resolveError = errors.New("API observation unavailable")
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err == nil {
		t.Fatal("ambiguous Pod resolution was acknowledged as a completed DEL")
	}
	if _, err := plugin.Store.Load(request.Key()); err != nil {
		t.Fatalf("ambiguous Pod resolution discarded durable state: %v", err)
	}
}

func TestDeleteWithMissingOrReusedNamespacePreservesForeignState(t *testing.T) {
	plugin, request, events := fixture(t)
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	enforcer := plugin.Enforcer.(*fakeEnforcer)
	enforcer.identities[request.Pod.NetNS] = "foreign"
	plugin.Agent.(*fakeAgent).resolution.Terminating = true
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err != nil {
		t.Fatal(err)
	}
	if index(*events, "cleanup") >= 0 {
		t.Fatalf("foreign namespace was cleaned: %v", *events)
	}
}

func TestCheckRequiresAgentAndObservedProtectedPath(t *testing.T) {
	plugin, request, events := fixture(t)
	if err := plugin.Add(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	*events = nil
	if err := plugin.Check(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"identity", "agent-check"}) {
		t.Fatalf("CHECK operations = %v", *events)
	}
	plugin.Agent.(*fakeAgent).checkError = errors.New("agent unavailable")
	if err := plugin.Check(context.Background(), request); err == nil {
		t.Fatal("CHECK succeeded without live agent observation")
	}
}

func TestGCKeepsValidAndRemovesOnlyExactStaleAttachments(t *testing.T) {
	plugin, valid, events := fixture(t)
	if err := plugin.Add(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	stale := valid
	stale.Pod.ContainerID = "sandbox-stale"
	stale.Pod.NetNS = "/netns/stale"
	plugin.Enforcer.(*fakeEnforcer).identities[stale.Pod.NetNS] = "12:35"
	if err := plugin.Add(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	*events = nil
	if err := plugin.GC(context.Background(), valid.Network, map[Key]struct{}{valid.Key(): {}}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Store.Load(valid.Key()); err != nil {
		t.Fatalf("valid state was removed: %v", err)
	}
	if _, err := plugin.Store.Load(stale.Key()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale state remains: %v", err)
	}
	if !reflect.DeepEqual(*events, []string{"identity", "cleanup"}) {
		t.Fatalf("GC operations = %v", *events)
	}
}

func fixture(t *testing.T) (Plugin, Request, *[]string) {
	t.Helper()
	events := &[]string{}
	request := Request{Network: "kindnet", Pod: PodIdentity{Namespace: "apps", Name: "protected", UID: "pod-uid", ContainerID: "sandbox-id", IfName: "eth0", NetNS: "/netns/current"}}
	binding := Binding{UID: "binding-uid", Generation: 1, PodUID: request.Pod.UID, GatewayUID: "gateway-uid"}
	agent := &fakeAgent{events: events, resolution: Resolution{PodUID: request.Pod.UID, Enrolled: true}, binding: binding, prepareWant: binding}
	enforcer := &fakeEnforcer{events: events, identities: map[string]string{request.Pod.NetNS: "12:34"}, identityErrors: map[string]error{}}
	plugin := Plugin{Agent: agent, Enforcer: enforcer, Store: FileStore{Directory: t.TempDir()}, ResolveTimeout: 20 * time.Millisecond, BindingTimeout: 20 * time.Millisecond, RetryInterval: time.Millisecond}
	return plugin, request, events
}

func index(values []string, value string) int {
	for i, candidate := range values {
		if candidate == value {
			return i
		}
	}
	return -1
}
