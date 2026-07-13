// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Amoenus/waycloak/internal/contract"
)

const APIVersion = "networking.waycloak.io/v1alpha1"

const (
	ApplicationPortModeFixed            = "Fixed"
	ApplicationPortModeProviderAssigned = "ProviderAssigned"
)

var (
	ErrRecordNotFound          = errors.New("port-forward delivery record not found")
	ErrAcknowledgementMismatch = errors.New("port-forward delivery acknowledgement does not match the current record")
)

type Record struct {
	Identity            string    `json:"identity"`
	Namespace           string    `json:"namespace"`
	Name                string    `json:"name"`
	State               string    `json:"state"`
	Gateway             string    `json:"gateway"`
	PublicPort          uint16    `json:"publicPort"`
	TargetPort          uint16    `json:"targetPort"`
	ApplicationPort     uint16    `json:"applicationPort"`
	ApplicationPortMode string    `json:"applicationPortMode"`
	Protocols           []string  `json:"protocols"`
	Generation          int64     `json:"generation"`
	IssuedAt            time.Time `json:"issuedAt"`
	RenewAfter          time.Time `json:"renewAfter"`
	ExpiresAt           time.Time `json:"expiresAt"`
}

type Document struct {
	APIVersion string   `json:"apiVersion"`
	PodUID     string   `json:"podUID"`
	Leases     []Record `json:"leases"`
}

type Observation struct {
	APIVersion  string    `json:"apiVersion"`
	Identity    string    `json:"identity"`
	PodUID      string    `json:"podUID"`
	Generation  int64     `json:"generation"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Ready       bool      `json:"ready"`
	AppliedPort uint16    `json:"appliedPort,omitempty"`
}

type ApplicationAcknowledgement struct {
	Generation      int64  `json:"generation"`
	ApplicationPort uint16 `json:"applicationPort"`
}

type PortRedirect struct {
	Identity        string
	Generation      int64
	TargetPort      uint16
	ApplicationPort uint16
	Protocols       []string
}

func (document Document) Validate(now time.Time) error {
	if document.APIVersion != APIVersion || document.PodUID == "" {
		return errors.New("port-forward delivery document identity is invalid")
	}
	identities := make(map[string]struct{}, len(document.Leases))
	for i := range document.Leases {
		record := &document.Leases[i]
		if record.ApplicationPortMode == "" {
			record.ApplicationPortMode = ApplicationPortModeFixed
		}
		if record.ApplicationPort == 0 && record.ApplicationPortMode == ApplicationPortModeFixed {
			record.ApplicationPort = record.TargetPort
		}
		if record.Identity == "" || record.Namespace == "" || record.Name == "" || record.State != "Active" || record.Gateway == "" || record.PublicPort == 0 || record.TargetPort == 0 || record.ApplicationPort == 0 || record.Generation < 1 || record.IssuedAt.IsZero() || record.RenewAfter.IsZero() || record.ExpiresAt.IsZero() || !record.IssuedAt.Before(record.ExpiresAt) || !record.RenewAfter.Before(record.ExpiresAt) || !now.Before(record.ExpiresAt) {
			return fmt.Errorf("port-forward delivery record %d is invalid", i)
		}
		if record.ApplicationPortMode != ApplicationPortModeFixed && record.ApplicationPortMode != ApplicationPortModeProviderAssigned || record.ApplicationPortMode == ApplicationPortModeFixed && record.ApplicationPort != record.TargetPort || record.ApplicationPortMode == ApplicationPortModeProviderAssigned && record.ApplicationPort != record.PublicPort {
			return fmt.Errorf("port-forward delivery record %d application port is invalid", i)
		}
		if _, exists := identities[record.Identity]; exists {
			return errors.New("port-forward delivery identity is duplicated")
		}
		identities[record.Identity] = struct{}{}
		if len(record.Protocols) == 0 || !slices.IsSorted(record.Protocols) {
			return errors.New("port-forward delivery protocols are invalid")
		}
		for protocolIndex, protocol := range record.Protocols {
			if protocol != "TCP" && protocol != "UDP" || protocolIndex > 0 && record.Protocols[protocolIndex-1] == protocol {
				return errors.New("port-forward delivery protocol is invalid")
			}
		}
	}
	if !slices.IsSortedFunc(document.Leases, func(a, b Record) int { return strings.Compare(a.Identity, b.Identity) }) {
		return errors.New("port-forward delivery records are not deterministically ordered")
	}
	return nil
}

func Marshal(document Document) (string, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func Load(directory string, now time.Time) (Document, error) {
	data, err := os.ReadFile(filepath.Join(directory, contract.PortForwardLeasesKey))
	if err != nil {
		return Document{}, fmt.Errorf("read port-forward delivery document: %w", err)
	}
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return Document{}, fmt.Errorf("decode port-forward delivery document: %w", err)
	}
	if err := document.Validate(now.UTC()); err != nil {
		return Document{}, err
	}
	return document, nil
}

type Store struct {
	Now func() time.Time

	mu        sync.RWMutex
	document  Document
	err       error
	requested map[string]ApplicationAcknowledgement
	applied   map[string]ApplicationAcknowledgement
}

func (store *Store) Refresh(directory string) error {
	document, err := Load(directory, store.now())
	store.mu.Lock()
	defer store.mu.Unlock()
	store.document = document
	store.err = err
	store.pruneAcknowledgementsLocked()
	return err
}

func (store *Store) Document() (Document, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.err != nil {
		return Document{}, store.err
	}
	document := store.document
	document.Leases = append([]Record(nil), document.Leases...)
	for i := range document.Leases {
		document.Leases[i].Protocols = append([]string(nil), document.Leases[i].Protocols...)
	}
	return document, nil
}

func (store *Store) Record(identity string) (Record, error) {
	document, err := store.Document()
	if err != nil {
		return Record{}, err
	}
	for _, record := range document.Leases {
		if record.Identity == identity {
			if !store.now().Before(record.ExpiresAt) {
				return Record{}, ErrRecordNotFound
			}
			return record, nil
		}
	}
	return Record{}, ErrRecordNotFound
}

func (store *Store) Observe(identity string) (Observation, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.err != nil {
		return Observation{}, store.err
	}
	for _, record := range store.document.Leases {
		if record.Identity == identity && store.now().Before(record.ExpiresAt) {
			observation := Observation{APIVersion: APIVersion, Identity: identity, PodUID: store.document.PodUID, Generation: record.Generation, ExpiresAt: record.ExpiresAt}
			if record.ApplicationPortMode == ApplicationPortModeFixed {
				observation.Ready = true
				observation.AppliedPort = record.ApplicationPort
				return observation, nil
			}
			ack := store.applied[identity]
			observation.Ready = ack.Generation == record.Generation && ack.ApplicationPort == record.ApplicationPort
			if observation.Ready {
				observation.AppliedPort = ack.ApplicationPort
			}
			return observation, nil
		}
	}
	return Observation{}, ErrRecordNotFound
}

func (store *Store) Acknowledge(identity string, acknowledgement ApplicationAcknowledgement) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.err != nil {
		return store.err
	}
	for _, record := range store.document.Leases {
		if record.Identity != identity {
			continue
		}
		if record.ApplicationPortMode == ApplicationPortModeProviderAssigned && record.Generation == acknowledgement.Generation && record.ApplicationPort == acknowledgement.ApplicationPort && store.now().Before(record.ExpiresAt) {
			if store.requested == nil {
				store.requested = map[string]ApplicationAcknowledgement{}
			}
			store.requested[identity] = acknowledgement
			return nil
		}
		return ErrAcknowledgementMismatch
	}
	return ErrRecordNotFound
}

func (store *Store) RequestedRedirects() []PortRedirect {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.err != nil {
		return nil
	}
	redirects := make([]PortRedirect, 0, len(store.requested))
	for _, record := range store.document.Leases {
		ack := store.requested[record.Identity]
		if record.ApplicationPortMode == ApplicationPortModeProviderAssigned && ack.Generation == record.Generation && ack.ApplicationPort == record.ApplicationPort && store.now().Before(record.ExpiresAt) {
			redirects = append(redirects, PortRedirect{Identity: record.Identity, Generation: record.Generation, TargetPort: record.TargetPort, ApplicationPort: record.ApplicationPort, Protocols: append([]string(nil), record.Protocols...)})
		}
	}
	return redirects
}

func (store *Store) MarkApplied(redirects []PortRedirect) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.applied = make(map[string]ApplicationAcknowledgement, len(redirects))
	for _, redirect := range redirects {
		store.applied[redirect.Identity] = ApplicationAcknowledgement{Generation: redirect.Generation, ApplicationPort: redirect.ApplicationPort}
	}
}

func (store *Store) ClearApplied() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.applied = nil
}

func (store *Store) pruneAcknowledgementsLocked() {
	valid := map[string]ApplicationAcknowledgement{}
	if store.err == nil {
		for _, record := range store.document.Leases {
			ack := store.requested[record.Identity]
			if record.ApplicationPortMode == ApplicationPortModeProviderAssigned && ack.Generation == record.Generation && ack.ApplicationPort == record.ApplicationPort && store.now().Before(record.ExpiresAt) {
				valid[record.Identity] = ack
			}
		}
	}
	store.requested = valid
	for identity, ack := range store.applied {
		if valid[identity] != ack {
			delete(store.applied, identity)
		}
	}
}

func (store *Store) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC()
	}
	return time.Now().UTC()
}
