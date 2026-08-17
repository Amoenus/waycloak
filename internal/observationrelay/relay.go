// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package observationrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/nodeagent"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const MaxObservations = 256
const ReportPath = "/node-observations/v1/report"
const kubernetesAudience = "https://kubernetes.default.svc"
const maxConcurrentObservationWrites = 8

type TokenReviewer interface {
	Create(context.Context, *authenticationv1.TokenReview, metav1.CreateOptions) (*authenticationv1.TokenReview, error)
}

type NodePublisher interface {
	Apply(context.Context, *corev1.Pod, nodeagent.NodeReport) error
}

type Relay struct {
	Reviewer            TokenReviewer
	Reader              client.Reader
	Writer              client.Client
	AgentNamespace      string
	AgentServiceAccount string
	NodePublisher       NodePublisher
	Now                 func() time.Time
	OperationHook       func(string, time.Duration, error)
}

func (r *Relay) Handler() http.Handler {
	return http.HandlerFunc(r.serve)
}

func (r *Relay) serve(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != ReportPath {
		http.NotFound(response, request)
		return
	}
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return
	}
	started := time.Now()
	agentPod, err := r.authenticate(request.Context(), token)
	r.observeOperation("authenticate", time.Since(started), err)
	if err != nil {
		http.Error(response, "authentication failed", http.StatusUnauthorized)
		return
	}
	var report nodeagent.Report
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil || report.APIVersion != nodeagent.ReportAPIVersion || len(report.Observations) > MaxObservations {
		http.Error(response, "invalid observation report", http.StatusBadRequest)
		return
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(response, "invalid observation report", http.StatusBadRequest)
		return
	}
	if r.NodePublisher == nil {
		http.Error(response, "node capability publisher unavailable", http.StatusServiceUnavailable)
		return
	}
	started = time.Now()
	err = r.applyReport(request.Context(), agentPod, report)
	r.observeOperation("transaction", time.Since(started), err)
	if err != nil {
		http.Error(response, "observation transaction rejected", http.StatusForbidden)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// applyReport keeps acknowledgement synchronous while allowing the independent
// node-capability and per-binding writes to share one bounded request budget.
// Any failed or timed-out write rejects the transaction; the reporting agent
// then locks down every durable attachment before it can advertise recovery.
func (r *Relay) applyReport(ctx context.Context, agentPod *corev1.Pod, report nodeagent.Report) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan nodeagent.Observation)
	errs := make(chan error, len(report.Observations)+1)
	var wait sync.WaitGroup
	run := func(operation string, apply func() error) {
		defer wait.Done()
		started := time.Now()
		err := apply()
		r.observeOperation(operation, time.Since(started), err)
		if err != nil {
			errs <- err
			cancel()
		}
	}

	wait.Add(1)
	go run("node_capability", func() error { return r.NodePublisher.Apply(ctx, agentPod, report.Node) })
	workers := min(maxConcurrentObservationWrites, len(report.Observations))
	for range workers {
		wait.Add(1)
		go run("binding_status", func() error {
			for observation := range jobs {
				if err := r.apply(ctx, agentPod, observation); err != nil {
					return err
				}
			}
			return nil
		})
	}
sendObservations:
	for _, observation := range report.Observations {
		select {
		case jobs <- observation:
		case <-ctx.Done():
			break sendObservations
		}
	}
	close(jobs)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Relay) observeOperation(operation string, elapsed time.Duration, err error) {
	if r.OperationHook != nil {
		r.OperationHook(operation, elapsed, err)
	}
}

func (r *Relay) authenticate(ctx context.Context, token string) (*corev1.Pod, error) {
	if r.Reviewer == nil || r.Reader == nil || r.Writer == nil {
		return nil, errors.New("relay dependencies are required")
	}
	review, err := r.Reviewer.Create(ctx, &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{kubernetesAudience}}}, metav1.CreateOptions{})
	if err != nil || !review.Status.Authenticated || !contains(review.Status.Audiences, kubernetesAudience) {
		return nil, errors.New("token is not authenticated")
	}
	podName := firstExtra(review.Status.User.Extra, "authentication.kubernetes.io/pod-name")
	podUID := firstExtra(review.Status.User.Extra, "authentication.kubernetes.io/pod-uid")
	podNamespace := serviceAccountNamespace(review.Status.User.Username)
	if podName == "" || podUID == "" || podNamespace == "" ||
		podNamespace != r.AgentNamespace || review.Status.User.Username != "system:serviceaccount:"+r.AgentNamespace+":"+r.AgentServiceAccount {
		return nil, errors.New("token is not bound to a Pod")
	}
	pod := &corev1.Pod{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: podNamespace, Name: podName}, pod); err != nil {
		return nil, err
	}
	if string(pod.UID) != podUID || pod.Spec.NodeName == "" || pod.Spec.ServiceAccountName != r.AgentServiceAccount || pod.Labels["app.kubernetes.io/component"] != "node-agent" {
		return nil, errors.New("token Pod is not a current node agent")
	}
	return pod, nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (r *Relay) apply(ctx context.Context, agentPod *corev1.Pod, observation nodeagent.Observation) error {
	if observation.BindingNamespace == "" || observation.BindingName == "" || observation.BindingUID == "" ||
		observation.Generation < 1 || observation.PodUID == "" || observation.GatewayUID == "" ||
		observation.NodeName != agentPod.Spec.NodeName || observation.NodeBootID == "" || observation.InstanceID == "" {
		return errors.New("observation identity is incomplete")
	}
	binding := &wayv1.VPNWorkloadBinding{}
	key := client.ObjectKey{Namespace: observation.BindingNamespace, Name: observation.BindingName}
	if err := r.Reader.Get(ctx, key, binding); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// The authenticated withdrawal can race finalizer removal. An exact
			// binding that is already absent needs no status mutation and must not
			// poison the node capability report.
			return nil
		}
		return err
	}
	if string(binding.UID) != observation.BindingUID || observation.Generation > binding.Generation ||
		string(binding.Spec.PodRef.UID) != observation.PodUID || string(binding.Spec.NodeName) != agentPod.Spec.NodeName {
		return errors.New("observation does not match current node-scoped binding")
	}
	if observation.Generation < binding.Generation {
		// Gateway replacement can advance the binding between observation and
		// relay. The authenticated old generation is a no-op; rejecting the
		// entire report would prevent the agent from reconciling the new intent.
		return nil
	}
	if string(binding.Spec.GatewayRef.UID) != observation.GatewayUID || (!binding.DeletionTimestamp.IsZero() && observation.Ready) {
		return errors.New("observation does not match current node-scoped binding")
	}
	updated := binding.DeepCopy()
	updated.Status.ObservedPodUID = binding.Spec.PodRef.UID
	updated.Status.ObservedGatewayUID = binding.Spec.GatewayRef.UID
	updated.Status.AppliedGeneration = 0
	if observation.Ready {
		updated.Status.AppliedGeneration = binding.Generation
	}
	updated.Status.Agent = &wayv1.NodeAgentObservation{
		NodeName: binding.Spec.NodeName, NodeBootID: observation.NodeBootID, InstanceID: observation.InstanceID,
		ObservedAt: metav1.NewTime(r.now()),
	}
	if err := r.Writer.Status().Patch(ctx, updated, client.MergeFrom(binding), client.FieldOwner(wayv1.FieldManagerBindingController)); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("relay authenticated node observation: %w", err)
	}
	return nil
}

func firstExtra(extra map[string]authenticationv1.ExtraValue, key string) string {
	values := extra[key]
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func serviceAccountNamespace(username string) string {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(username, prefix), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0]
}

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
