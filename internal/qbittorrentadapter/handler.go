// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrentadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	wayportforward "github.com/Amoenus/waycloak/internal/portforward"
)

const adapterPathPrefix = "/networking.waycloak.io/adapter/v1/leases/"

type ApplicationConfigurer interface {
	Apply(context.Context, netip.Addr, uint16, bool) error
	Observe(context.Context, netip.Addr, uint16, []string) error
}

type Handler struct {
	Application              ApplicationConfigurer
	Namespace                wayv1.NamespaceName
	Name                     wayv1.ObjectName
	Image                    string
	PodUID                   wayv1.ObjectUID
	StateFile                string
	HealthIdentity           string
	RuntimeIdentity          string
	Now                      func() time.Time
	ObservationBudget        time.Duration
	ObservationRetryInterval time.Duration

	mu     sync.Mutex
	states map[wayv1.ObjectUID]adapterState
}

type adapterState struct {
	Record        wayportforward.AdapterLeaseRecord `json:"record"`
	AppliedAt     time.Time                         `json:"appliedAt"`
	ObservedAt    time.Time                         `json:"observedAt,omitempty"`
	AdapterPodUID wayv1.ObjectUID                   `json:"adapterPodUID,omitempty"`
}

func (h *Handler) Load() error {
	if h.StateFile == "" {
		h.states = map[wayv1.ObjectUID]adapterState{}
		return nil
	}
	contents, err := os.ReadFile(h.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		h.states = map[wayv1.ObjectUID]adapterState{}
		return nil
	}
	if err != nil {
		return err
	}
	states := map[wayv1.ObjectUID]adapterState{}
	if err := decodeStrict(contents, &states); err != nil {
		return errors.New("adapter state is invalid")
	}
	h.states = states
	return nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	if request.URL.RawQuery != "" || h.Application == nil || h.Namespace == "" || h.Name == "" || h.Image == "" || h.PodUID == "" {
		writeError(response, http.StatusBadRequest)
		return
	}
	if request.URL.Path == "/networking.waycloak.io/adapter/v1/healthz" {
		if h.HealthIdentity != "" && !wayportforward.PeerHasIdentity(request, h.HealthIdentity) {
			writeError(response, http.StatusForbidden)
			return
		}
		if request.Method != http.MethodGet {
			writeError(response, http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(response).Encode(wayportforward.AdapterHealthObservation{APIVersion: wayportforward.AdapterAPIVersion,
			Namespace: h.Namespace, Name: h.Name, Image: h.Image, PodUID: h.PodUID, ObservedAt: h.now(), Ready: true})
		return
	}
	leaseUID, operation, ok := parsePath(request.URL)
	if h.RuntimeIdentity != "" && !wayportforward.PeerHasIdentity(request, h.RuntimeIdentity) {
		writeError(response, http.StatusForbidden)
		return
	}
	if !ok || request.Header.Get("Content-Type") != "application/json" {
		writeError(response, http.StatusNotFound)
		return
	}
	body, err := readBounded(request.Body)
	if err != nil {
		writeError(response, http.StatusRequestEntityTooLarge)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.states == nil {
		if err := h.Load(); err != nil {
			writeError(response, http.StatusServiceUnavailable)
			return
		}
	}
	switch {
	case operation == "" && request.Method == http.MethodPut:
		h.deliver(response, request, leaseUID, body)
	case operation == "withdraw" && request.Method == http.MethodPost:
		h.withdraw(response, request, leaseUID, body)
	default:
		writeError(response, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) deliver(response http.ResponseWriter, request *http.Request, leaseUID wayv1.ObjectUID, body []byte) {
	var record wayportforward.AdapterLeaseRecord
	if decodeStrict(body, &record) != nil || !h.validRecord(record, leaseUID) {
		writeError(response, http.StatusBadRequest)
		return
	}
	previous, exists := h.states[leaseUID]
	if exists && (record.HandoffGeneration < previous.Record.HandoffGeneration || record.HandoffGeneration == previous.Record.HandoffGeneration &&
		(record.PodUID != previous.Record.PodUID || record.ApplicationAddress != previous.Record.ApplicationAddress || record.TargetPort != previous.Record.TargetPort)) {
		writeError(response, http.StatusConflict)
		return
	}
	identityEqual := exists && sameApplicationIdentity(record, previous.Record)
	if identityEqual && previous.AdapterPodUID == h.PodUID && !record.ExpiresAt.Equal(previous.Record.ExpiresAt) && !previous.AppliedAt.IsZero() {
		now := h.now()
		renewed := previous
		renewed.Record = record
		renewed.ObservedAt = now
		h.states[leaseUID] = renewed
		if err := h.persist(); err != nil {
			h.states[leaseUID] = previous
			writeError(response, http.StatusServiceUnavailable)
			return
		}
		h.writeAcknowledgement(response, record, now)
		return
	}
	observedAt := previous.ObservedAt
	if observedAt.IsZero() {
		observedAt = previous.AppliedAt
	}
	if identityEqual && previous.AdapterPodUID == h.PodUID && recordsEqual(record, previous.Record) && observedAt.After(h.now().Add(-wayportforward.DefaultObservationFreshness)) {
		h.writeAcknowledgement(response, record, observedAt)
		return
	}
	needsApply := !identityEqual || previous.AppliedAt.IsZero()
	pending := adapterState{Record: record}
	if !needsApply {
		pending.AppliedAt = previous.AppliedAt
	}
	h.states[leaseUID] = pending
	if err := h.persist(); err != nil {
		h.restoreState(leaseUID, previous, exists)
		slog.Error("qBittorrent adapter could not persist delivery intent", "lease_uid", leaseUID, "error", err)
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	if needsApply {
		if err := h.Application.Apply(request.Context(), record.ApplicationAddress, record.TargetPort, true); err != nil {
			slog.Warn("qBittorrent adapter could not apply delivery", "lease_uid", leaseUID, "failure_kind", "unavailable", "error", err)
			writeError(response, http.StatusServiceUnavailable)
			return
		}
		pending.AppliedAt = h.now()
		h.states[leaseUID] = pending
		if err := h.persist(); err != nil {
			slog.Error("qBittorrent adapter could not persist applied mutation", "lease_uid", leaseUID, "error", err)
			writeError(response, http.StatusServiceUnavailable)
			return
		}
	}
	if err := h.observeWithRetry(request.Context(), record.ApplicationAddress, record.TargetPort); err != nil {
		slog.Warn("qBittorrent adapter could not observe delivery", "lease_uid", leaseUID, "failure_kind", "unavailable", "error", err)
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	now := h.now()
	h.states[leaseUID] = adapterState{Record: record, AppliedAt: pending.AppliedAt, ObservedAt: now, AdapterPodUID: h.PodUID}
	if err := h.persist(); err != nil {
		slog.Error("qBittorrent adapter could not persist applied delivery", "lease_uid", leaseUID, "error", err)
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	h.writeAcknowledgement(response, record, now)
}

func (h *Handler) withdraw(response http.ResponseWriter, request *http.Request, leaseUID wayv1.ObjectUID, body []byte) {
	var intent wayportforward.AdapterWithdrawalIntent
	if decodeStrict(body, &intent) != nil || intent.APIVersion != wayportforward.AdapterAPIVersion || intent.LeaseNamespace != h.Namespace || intent.LeaseUID != leaseUID ||
		intent.HandoffGeneration < 1 || intent.PodUID == "" {
		writeError(response, http.StatusBadRequest)
		return
	}
	state, exists := h.states[leaseUID]
	if !exists {
		h.writeWithdrawalAcknowledgement(response, intent)
		return
	}
	if state.Record.HandoffGeneration != intent.HandoffGeneration || state.Record.PodUID != intent.PodUID {
		writeError(response, http.StatusConflict)
		return
	}
	if err := h.Application.Apply(request.Context(), state.Record.ApplicationAddress, state.Record.BackendPort, true); err != nil {
		if !intent.ApplicationEndpointRetired {
			slog.Warn("qBittorrent adapter could not restore backend port", "lease_uid", leaseUID, "error", err)
			writeError(response, http.StatusServiceUnavailable)
			return
		}
		slog.Info("qBittorrent adapter cleared retired endpoint after backend-port restoration became impossible", "lease_uid", leaseUID,
			"handoff_generation", intent.HandoffGeneration, "pod_uid", intent.PodUID)
	} else if err := h.observeWithRetry(request.Context(), state.Record.ApplicationAddress, state.Record.BackendPort); err != nil && !intent.ApplicationEndpointRetired {
		slog.Warn("qBittorrent adapter could not observe restored backend port", "lease_uid", leaseUID, "failure_kind", "unavailable", "error", err)
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	delete(h.states, leaseUID)
	if err := h.persist(); err != nil {
		h.states[leaseUID] = state
		slog.Error("qBittorrent adapter could not persist withdrawal", "lease_uid", leaseUID, "error", err)
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	h.writeWithdrawalAcknowledgement(response, intent)
}

func (h *Handler) observeWithRetry(ctx context.Context, address netip.Addr, port uint16) error {
	budget := h.ObservationBudget
	maximum := wayportforward.DefaultObservationFreshness - time.Second
	if budget <= 0 || budget > maximum {
		budget = maximum / 2
	}
	interval := h.ObservationRetryInterval
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	observationContext, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var lastErr error
	for {
		lastErr = h.Application.Observe(observationContext, address, port, []string{"tcp", "udp"})
		if lastErr == nil {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-observationContext.Done():
			timer.Stop()
			return fmt.Errorf("listener observation deadline: %w", lastErr)
		case <-timer.C:
		}
	}
}

func sameApplicationIdentity(left, right wayportforward.AdapterLeaseRecord) bool {
	return left.LeaseUID == right.LeaseUID && left.HandoffGeneration == right.HandoffGeneration && left.PodUID == right.PodUID &&
		left.PublicAddress == right.PublicAddress && left.PublicPort == right.PublicPort && left.TargetPort == right.TargetPort &&
		left.ApplicationAddress == right.ApplicationAddress && left.BackendPort == right.BackendPort && protocolsEqual(left.Protocols, right.Protocols)
}

func recordsEqual(left, right wayportforward.AdapterLeaseRecord) bool {
	return sameApplicationIdentity(left, right) && left.APIVersion == right.APIVersion && left.LeaseNamespace == right.LeaseNamespace && left.ExpiresAt.Equal(right.ExpiresAt)
}

func protocolsEqual(left, right []wayv1.TransportProtocol) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (h *Handler) writeWithdrawalAcknowledgement(response http.ResponseWriter, intent wayportforward.AdapterWithdrawalIntent) {
	_ = json.NewEncoder(response).Encode(wayportforward.AdapterWithdrawalAcknowledgement{APIVersion: wayportforward.AdapterAPIVersion,
		LeaseNamespace: intent.LeaseNamespace, LeaseUID: intent.LeaseUID, HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID,
		ObservedAt: h.now(), Withdrawn: true})
}

func (h *Handler) restoreState(leaseUID wayv1.ObjectUID, previous adapterState, existed bool) {
	if existed {
		h.states[leaseUID] = previous
		return
	}
	delete(h.states, leaseUID)
}

func (h *Handler) validRecord(record wayportforward.AdapterLeaseRecord, leaseUID wayv1.ObjectUID) bool {
	if record.APIVersion != wayportforward.AdapterAPIVersion || record.LeaseNamespace != h.Namespace || record.LeaseUID != leaseUID || record.HandoffGeneration < 1 ||
		record.PodUID == "" || !record.ApplicationAddress.Is4() || !record.PublicAddress.IsValid() || !record.PublicAddress.IsGlobalUnicast() || record.PublicPort == 0 ||
		record.TargetPort != record.PublicPort || record.BackendPort == 0 || !record.ExpiresAt.After(h.now()) {
		return false
	}
	tcp, udp := false, false
	for _, protocol := range record.Protocols {
		switch protocol {
		case wayv1.ProtocolTCP:
			tcp = true
		case wayv1.ProtocolUDP:
			udp = true
		default:
			return false
		}
	}
	return tcp && udp && len(record.Protocols) == 2
}

func (h *Handler) writeAcknowledgement(response http.ResponseWriter, record wayportforward.AdapterLeaseRecord, observedAt time.Time) {
	_ = json.NewEncoder(response).Encode(wayportforward.AdapterAcknowledgement{APIVersion: wayportforward.AdapterAPIVersion,
		LeaseNamespace: record.LeaseNamespace, LeaseUID: record.LeaseUID, HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID,
		ObservedAt: observedAt, ExpiresAt: record.ExpiresAt})
}

func (h *Handler) persist() error {
	if h.StateFile == "" {
		return nil
	}
	directory := filepath.Dir(h.StateFile)
	temporary, err := os.CreateTemp(directory, ".waycloak-qbittorrent-state-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(h.states); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFile(temporaryName, h.StateFile)
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

func parsePath(value *url.URL) (wayv1.ObjectUID, string, bool) {
	if value == nil || !strings.HasPrefix(value.EscapedPath(), adapterPathPrefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(value.EscapedPath(), adapterPathPrefix), "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		return "", "", false
	}
	uid, err := url.PathUnescape(parts[0])
	if err != nil || uid == "" || strings.Contains(uid, "/") || len(uid) > 128 {
		return "", "", false
	}
	operation := ""
	if len(parts) == 2 {
		operation = parts[1]
		if operation != "withdraw" {
			return "", "", false
		}
	}
	return wayv1.ObjectUID(uid), operation, true
}

func readBounded(reader io.Reader) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, responseLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > responseLimit {
		return nil, errors.New("adapter request exceeds size limit")
	}
	return contents, nil
}

func decodeStrict(contents []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("adapter request contains trailing data")
	}
	return nil
}

func writeError(response http.ResponseWriter, status int) {
	response.WriteHeader(status)
	_, _ = response.Write([]byte(`{"apiVersion":"networking.waycloak.io/adapter/v1","error":"request rejected"}`))
}
