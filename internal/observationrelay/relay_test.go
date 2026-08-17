// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package observationrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/nodeagent"
	"github.com/Amoenus/waycloak/internal/scheduling"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type coordinatedPublisher struct {
	started        chan struct{}
	bindingStarted chan struct{}
	once           sync.Once
}

func (p *coordinatedPublisher) Apply(ctx context.Context, _ *corev1.Pod, _ nodeagent.NodeReport) error {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.bindingStarted:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type coordinatedClient struct {
	client.Client
	publisherStarted chan struct{}
	bindingStarted   chan struct{}
	once             sync.Once
}

func (c *coordinatedClient) Status() client.SubResourceWriter {
	return coordinatedStatusWriter{SubResourceWriter: c.Client.Status(), publisherStarted: c.publisherStarted, bindingStarted: c.bindingStarted, once: &c.once}
}

type coordinatedStatusWriter struct {
	client.SubResourceWriter
	publisherStarted chan struct{}
	bindingStarted   chan struct{}
	once             *sync.Once
}

func (w coordinatedStatusWriter) Patch(ctx context.Context, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
	w.once.Do(func() { close(w.bindingStarted) })
	select {
	case <-w.publisherStarted:
		return w.SubResourceWriter.Patch(ctx, object, patch, options...)
	case <-ctx.Done():
		return ctx.Err()
	}
}

type fakeReviewer struct {
	status authenticationv1.TokenReviewStatus
}

func (r fakeReviewer) Create(context.Context, *authenticationv1.TokenReview, metav1.CreateOptions) (*authenticationv1.TokenReview, error) {
	return &authenticationv1.TokenReview{Status: r.status}, nil
}

func TestRelayBindsPodTokenToExactNodeAndBinding(t *testing.T) {
	relay, kube, observation := fixture(t)
	response := report(t, relay, observation)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	binding := &wayv1.VPNWorkloadBinding{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "apps", Name: "binding"}, binding); err != nil {
		t.Fatal(err)
	}
	if binding.Status.AppliedGeneration != 3 || binding.Status.Agent == nil || binding.Status.Agent.NodeName != "node-a" || !binding.Status.Agent.ObservedAt.Time.Equal(time.Unix(2000, 0)) {
		t.Fatalf("relayed status = %#v", binding.Status)
	}
	node := &corev1.Node{}
	if err := kube.Get(context.Background(), client.ObjectKey{Name: "node-a"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[scheduling.CNIReadyLabel] != "true" {
		t.Fatalf("authenticated node readiness was not published: %#v", node.Labels)
	}
}

func TestRelayAppliesIndependentNodeAndBindingWritesConcurrently(t *testing.T) {
	relay, kube, observation := fixture(t)
	publisherStarted := make(chan struct{})
	bindingStarted := make(chan struct{})
	relay.NodePublisher = &coordinatedPublisher{started: publisherStarted, bindingStarted: bindingStarted}
	relay.Writer = &coordinatedClient{Client: kube, publisherStarted: publisherStarted, bindingStarted: bindingStarted}

	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() { completed <- report(t, relay, observation) }()
	select {
	case response := <-completed:
		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("node capability and binding status writes did not overlap")
	}
}

func TestRelayRejectsCrossNodeForgery(t *testing.T) {
	relay, _, observation := fixture(t)
	observation.NodeName = "node-b"
	if response := report(t, relay, observation); response.Code != http.StatusForbidden {
		t.Fatalf("cross-node status = %d", response.Code)
	}
}

func TestRelayIgnoresObservationForBindingAlreadyDeleted(t *testing.T) {
	relay, kube, observation := fixture(t)
	binding := &wayv1.VPNWorkloadBinding{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: observation.BindingNamespace, Name: observation.BindingName}, binding); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if response := report(t, relay, observation); response.Code != http.StatusNoContent {
		t.Fatalf("already-absent binding status = %d, body %s", response.Code, response.Body.String())
	}
	node := &corev1.Node{}
	if err := kube.Get(context.Background(), client.ObjectKey{Name: "node-a"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[scheduling.CNIReadyLabel] != "true" {
		t.Fatalf("obsolete binding observation poisoned node readiness: %#v", node.Labels)
	}
}

func TestRelayIgnoresOlderGenerationForSameBindingPodAndNode(t *testing.T) {
	relay, kube, observation := fixture(t)
	binding := &wayv1.VPNWorkloadBinding{}
	key := client.ObjectKey{Namespace: observation.BindingNamespace, Name: observation.BindingName}
	if err := kube.Get(context.Background(), key, binding); err != nil {
		t.Fatal(err)
	}
	binding.Spec.Network.MTU = 1400
	binding.Generation = observation.Generation + 1
	if err := kube.Update(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), key, binding); err != nil {
		t.Fatal(err)
	}
	if binding.Generation <= observation.Generation {
		t.Fatalf("fixture generation did not advance: binding=%d observation=%d", binding.Generation, observation.Generation)
	}
	if response := report(t, relay, observation); response.Code != http.StatusNoContent {
		t.Fatalf("older exact generation status = %d, body %s", response.Code, response.Body.String())
	}
	if err := kube.Get(context.Background(), key, binding); err != nil {
		t.Fatal(err)
	}
	if binding.Status.AppliedGeneration != 0 {
		t.Fatalf("older generation mutated current status: %#v", binding.Status)
	}
}

func TestRelayRejectsUnboundServiceAccountToken(t *testing.T) {
	relay, _, observation := fixture(t)
	relay.Reviewer = fakeReviewer{status: authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: "system:serviceaccount:waycloak-system:waycloak-node-agent"}}}
	if response := report(t, relay, observation); response.Code != http.StatusUnauthorized {
		t.Fatalf("unbound-token status = %d", response.Code)
	}
}

func TestRelayRejectsAnotherServiceAccount(t *testing.T) {
	relay, _, observation := fixture(t)
	relay.Reviewer = fakeReviewer{status: authenticationv1.TokenReviewStatus{Authenticated: true, Audiences: []string{kubernetesAudience}, User: authenticationv1.UserInfo{
		Username: "system:serviceaccount:waycloak-system:attacker",
		Extra: map[string]authenticationv1.ExtraValue{
			"authentication.kubernetes.io/pod-name": {"agent-node-a"},
			"authentication.kubernetes.io/pod-uid":  {"agent-pod-uid"},
		},
	}}}
	if response := report(t, relay, observation); response.Code != http.StatusUnauthorized {
		t.Fatalf("other-service-account status = %d", response.Code)
	}
}

func fixture(t *testing.T) (*Relay, client.Client, nodeagent.Observation) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agentPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent-node-a", Namespace: "waycloak-system", UID: "agent-pod-uid", Labels: map[string]string{"app.kubernetes.io/component": "node-agent"}}, Spec: corev1.PodSpec{NodeName: "node-a", ServiceAccountName: "waycloak-node-agent"}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
	binding := &wayv1.VPNWorkloadBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding", Namespace: "apps", UID: "binding-uid", Generation: 3}, Spec: wayv1.VPNWorkloadBindingSpec{
		PodRef: wayv1.LocalUIDReference{Name: "protected", UID: "pod-uid"}, GatewayRef: wayv1.NamespacedUIDReference{Namespace: "network", Name: "gateway", UID: "gateway-uid"}, NodeName: "node-a",
	}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(binding).WithObjects(agentPod, node, binding).Build()
	reviewer := fakeReviewer{status: authenticationv1.TokenReviewStatus{Authenticated: true, Audiences: []string{kubernetesAudience}, User: authenticationv1.UserInfo{
		Username: "system:serviceaccount:waycloak-system:waycloak-node-agent",
		Extra: map[string]authenticationv1.ExtraValue{
			"authentication.kubernetes.io/pod-name": {"agent-node-a"},
			"authentication.kubernetes.io/pod-uid":  {"agent-pod-uid"},
		},
	}}}
	release := wayv1.ReleaseIdentity{Version: "v1.0.0-beta.1", ManifestDigest: "sha256:4444444444444444444444444444444444444444444444444444444444444444"}
	relay := &Relay{Reviewer: reviewer, Reader: kube, Writer: kube, AgentNamespace: "waycloak-system", AgentServiceAccount: "waycloak-node-agent", Now: func() time.Time { return time.Unix(2000, 0).UTC() }, NodePublisher: &scheduling.Publisher{Client: kube, ReleaseIdentity: release, ConformanceProfile: "networking.waycloak.io/Core-v1", Now: func() time.Time { return time.Unix(2000, 0).UTC() }}}
	observation := nodeagent.Observation{BindingNamespace: "apps", BindingName: "binding", BindingUID: "binding-uid", Generation: 3, PodUID: "pod-uid", GatewayUID: "gateway-uid", NodeName: "node-a", NodeBootID: "boot", InstanceID: "instance", Ready: true}
	return relay, kube, observation
}

func report(t *testing.T, relay *Relay, observation nodeagent.Observation) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(nodeagent.Report{APIVersion: nodeagent.ReportAPIVersion, Node: nodeagent.NodeReport{
		NodeName: "node-a", NodeBootID: "boot", InstanceID: "instance", ObservedAt: time.Unix(2000, 0).UTC(), Ready: true,
		Capabilities: scheduling.BaselineCapabilities, ReleaseIdentity: wayv1.ReleaseIdentity{Version: "v1.0.0-beta.1", ManifestDigest: "sha256:4444444444444444444444444444444444444444444444444444444444444444"}, ConformanceProfile: "networking.waycloak.io/Core-v1",
	}, Observations: []nodeagent.Observation{observation}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ReportPath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer pod-bound-token")
	response := httptest.NewRecorder()
	relay.Handler().ServeHTTP(response, request)
	return response
}
