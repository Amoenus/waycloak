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

var ErrRecordNotFound = errors.New("port-forward delivery record not found")

type Record struct {
	Identity   string    `json:"identity"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	State      string    `json:"state"`
	Gateway    string    `json:"gateway"`
	PublicPort uint16    `json:"publicPort"`
	TargetPort uint16    `json:"targetPort"`
	Protocols  []string  `json:"protocols"`
	Generation int64     `json:"generation"`
	IssuedAt   time.Time `json:"issuedAt"`
	RenewAfter time.Time `json:"renewAfter"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type Document struct {
	APIVersion string   `json:"apiVersion"`
	PodUID     string   `json:"podUID"`
	Leases     []Record `json:"leases"`
}

type Observation struct {
	APIVersion string    `json:"apiVersion"`
	Identity   string    `json:"identity"`
	PodUID     string    `json:"podUID"`
	Generation int64     `json:"generation"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Ready      bool      `json:"ready"`
}

func (document Document) Validate(now time.Time) error {
	if document.APIVersion != APIVersion || document.PodUID == "" {
		return errors.New("port-forward delivery document identity is invalid")
	}
	identities := make(map[string]struct{}, len(document.Leases))
	for i := range document.Leases {
		record := &document.Leases[i]
		if record.Identity == "" || record.Namespace == "" || record.Name == "" || record.State != "Active" || record.Gateway == "" || record.PublicPort == 0 || record.TargetPort == 0 || record.Generation < 1 || record.IssuedAt.IsZero() || record.RenewAfter.IsZero() || record.ExpiresAt.IsZero() || !record.IssuedAt.Before(record.ExpiresAt) || !record.RenewAfter.Before(record.ExpiresAt) || !now.Before(record.ExpiresAt) {
			return fmt.Errorf("port-forward delivery record %d is invalid", i)
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

	mu       sync.RWMutex
	document Document
	err      error
}

func (store *Store) Refresh(directory string) error {
	document, err := Load(directory, store.now())
	store.mu.Lock()
	defer store.mu.Unlock()
	store.document = document
	store.err = err
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
			return Observation{APIVersion: APIVersion, Identity: identity, PodUID: store.document.PodUID, Generation: record.Generation, ExpiresAt: record.ExpiresAt, Ready: true}, nil
		}
	}
	return Observation{}, ErrRecordNotFound
}

func (store *Store) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC()
	}
	return time.Now().UTC()
}
