// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/dataplane"
)

type fakeAgent struct {
	mu            sync.Mutex
	events        *[]string
	resolution    Resolution
	binding       Binding
	bindingErrors []error
	resolveError  error
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

func (a *fakeAgent) Check(context.Context, PodIdentity) error {
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
	configureError error
	verifyError    error
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

func (e *fakeEnforcer) Configure(context.Context, string, dataplane.Config) error {
	e.record("configure")
	return e.configureError
}

func (e *fakeEnforcer) Verify(context.Context, string, dataplane.Config) error {
	e.record("verify")
	return e.verifyError
}

func (e *fakeEnforcer) Cleanup(context.Context, string, string) error {
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
	want := []string{"identity", "resolve", "lockdown", "binding", "binding", "binding", "configure", "verify"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("operation order = %v, want %v", *events, want)
	}
	attachment, err := plugin.Store.Load(request.Key())
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Phase != PhaseReady || attachment.Config == nil || attachment.Config.PodUID != request.Pod.UID {
		t.Fatalf("ready attachment = %#v", attachment)
	}
}

func TestAddFailureAfterPrimaryCNIRetainsDenyForDEL(t *testing.T) {
	plugin, request, events := fixture(t)
	plugin.Enforcer.(*fakeEnforcer).configureError = errors.New("injected programming failure")
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("programming failure unexpectedly succeeded")
	}
	want := []string{"identity", "resolve", "lockdown", "binding", "configure"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("operation order = %v, want %v", *events, want)
	}
	attachment, err := plugin.Store.Load(request.Key())
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Phase != PhaseLockedDown || attachment.Config == nil {
		t.Fatalf("partial attachment did not retain deny state: %#v", attachment)
	}
	plugin.Agent.(*fakeAgent).resolution.Terminating = true
	if err := plugin.Delete(context.Background(), request.Key(), request.Pod.NetNS); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Store.Load(request.Key()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state remained after DEL: %v", err)
	}
	if !reflect.DeepEqual((*events)[len(*events)-4:], []string{"resolve", "identity", "cleanup", "withdraw"}) {
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
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("binding wait was not bounded: %s", elapsed)
	}
	if _, err := plugin.Store.Load(request.Key()); err != nil {
		t.Fatalf("deny state was not retained: %v", err)
	}
	if index(*events, "lockdown") > index(*events, "binding") {
		t.Fatalf("binding was checked before lockdown: %v", *events)
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
	if got := (*events)[before:]; !reflect.DeepEqual(got, []string{"identity", "agent-check", "verify"}) {
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
	enforcer := plugin.Enforcer.(*fakeEnforcer)
	enforcer.configureError = errors.New("injected programming failure")
	if err := plugin.Add(context.Background(), request); err == nil {
		t.Fatal("initial ADD failure unexpectedly succeeded")
	}
	enforcer.configureError = nil
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
	plugin.Enforcer.(*fakeEnforcer).configureError = errors.New("programming unavailable")
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
	plugin.Enforcer.(*fakeEnforcer).configureError = nil
	*events = nil
	if err := plugin.Add(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	if index(*events, "resolve") >= 0 || index(*events, "lockdown") < 0 || index(*events, "binding") < 0 {
		t.Fatalf("UID-bound enrollment was not retained across sandbox replacement: %v", *events)
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
	if !reflect.DeepEqual(*events, []string{"identity", "agent-check", "verify"}) {
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
	cfg := dataplane.Config{
		PodUID: request.Pod.UID, Address: netip.MustParsePrefix("172.30.99.2/24"), OverlayCIDR: netip.MustParsePrefix("172.30.99.0/24"),
		GatewayAddress: netip.MustParseAddr("172.30.99.1"), GatewayEndpoint: netip.MustParseAddrPort("192.0.2.10:4789"),
		GatewayHealthPort: 18080, VNI: 7999, MTU: 1320, ClusterTrafficMode: dataplane.ClusterTrafficGateway,
	}
	agent := &fakeAgent{events: events, resolution: Resolution{PodUID: request.Pod.UID, Enrolled: true}, binding: Binding{PodUID: request.Pod.UID, Config: cfg}}
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
