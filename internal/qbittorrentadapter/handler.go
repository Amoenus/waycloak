// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrentadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	wayportforward "github.com/Amoenus/waycloak/internal/portforward"
)

const adapterPathPrefix = "/networking.waycloak.io/adapter/v1/leases/"

type ApplicationConfigurer interface {
	Configure(context.Context, netip.Addr, uint16, bool) error
}

type Handler struct {
	Application     ApplicationConfigurer
	Namespace       wayv1.NamespaceName
	Name            wayv1.ObjectName
	Image           string
	PodUID          wayv1.ObjectUID
	StateFile       string
	HealthIdentity  string
	RuntimeIdentity string
	Now             func() time.Time

	mu     sync.Mutex
	states map[wayv1.ObjectUID]adapterState
}

type adapterState struct {
	Record    wayportforward.AdapterLeaseRecord `json:"record"`
	AppliedAt time.Time                         `json:"appliedAt"`
	verified  bool
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
	reannounce := !exists || !previous.verified || record.HandoffGeneration != previous.Record.HandoffGeneration || record.PodUID != previous.Record.PodUID ||
		record.ApplicationAddress != previous.Record.ApplicationAddress || record.TargetPort != previous.Record.TargetPort
	if !reannounce && reflect.DeepEqual(record, previous.Record) && previous.AppliedAt.After(h.now().Add(-wayportforward.DefaultObservationFreshness)) {
		h.writeAcknowledgement(response, record, previous.AppliedAt)
		return
	}
	h.states[leaseUID] = adapterState{Record: record}
	if err := h.persist(); err != nil {
		h.restoreState(leaseUID, previous, exists)
		slog.Error("qBittorrent adapter could not persist delivery intent", "lease_uid", leaseUID, "error", err)
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	if err := h.Application.Configure(request.Context(), record.ApplicationAddress, record.TargetPort, reannounce); err != nil {
		slog.Warn("qBittorrent adapter could not apply delivery", "lease_uid", leaseUID, "error", err)
		writeError(response, http.StatusConflict)
		return
	}
	now := h.now()
	h.states[leaseUID] = adapterState{Record: record, AppliedAt: now, verified: true}
	if err := h.persist(); err != nil {
		slog.Error("qBittorrent adapter could not persist applied delivery", "lease_uid", leaseUID, "error", err)
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	h.writeAcknowledgement(response, record, now)
}

func (h *Handler) withdraw(response http.ResponseWriter, request *http.Request, leaseUID wayv1.ObjectUID, body []byte) {
	var intent wayportforward.WithdrawalIntent
	if decodeStrict(body, &intent) != nil || intent.APIVersion != wayportforward.RuntimeAPIVersion || intent.LeaseNamespace != h.Namespace || intent.LeaseUID != leaseUID ||
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
	if err := h.Application.Configure(request.Context(), state.Record.ApplicationAddress, state.Record.BackendPort, true); err != nil {
		slog.Warn("qBittorrent adapter could not restore backend port", "lease_uid", leaseUID, "error", err)
		writeError(response, http.StatusConflict)
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

func (h *Handler) writeWithdrawalAcknowledgement(response http.ResponseWriter, intent wayportforward.WithdrawalIntent) {
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
