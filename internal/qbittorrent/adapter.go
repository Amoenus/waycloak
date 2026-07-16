// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Amoenus/waycloak/internal/delivery"
)

type Adapter struct {
	Client        *Client
	LeaseEndpoint string
	LeaseName     string
	HTTP          *http.Client
	Now           func() time.Time
}

type LeaseRevision struct {
	Identity        string
	Generation      int64
	ApplicationPort uint16
}

type FailureKind string

const (
	FailureCritical                    FailureKind = "Critical"
	FailureTransientControlObservation FailureKind = "TransientControlObservation"
)

type ReconcileError struct {
	Kind     FailureKind
	Revision LeaseRevision
	Err      error
}

func (reconcileError *ReconcileError) Error() string { return reconcileError.Err.Error() }
func (reconcileError *ReconcileError) Unwrap() error { return reconcileError.Err }

func (adapter *Adapter) Reconcile(ctx context.Context) (LeaseRevision, error) {
	document, err := adapter.document(ctx)
	if err != nil {
		return LeaseRevision{}, critical(LeaseRevision{}, err)
	}
	var selected *delivery.Record
	for i := range document.Leases {
		record := &document.Leases[i]
		if record.ApplicationPortMode != delivery.ApplicationPortModeProviderAssigned || adapter.LeaseName != "" && record.Name != adapter.LeaseName {
			continue
		}
		if selected != nil {
			return LeaseRevision{}, critical(LeaseRevision{}, errors.New("multiple provider-assigned leases match the qBitTorrent adapter"))
		}
		selected = record
	}
	if selected == nil || !adapter.now().Before(selected.ExpiresAt) {
		return LeaseRevision{}, critical(LeaseRevision{}, errors.New("a current provider-assigned lease is unavailable"))
	}
	revision := LeaseRevision{Identity: selected.Identity, Generation: selected.Generation, ApplicationPort: selected.ApplicationPort}
	if adapter.Client == nil {
		return revision, critical(revision, errors.New("qBitTorrent client is required"))
	}
	current, err := adapter.Client.ListenPort(ctx)
	if err != nil {
		if transientControlObservation(err) {
			return revision, &ReconcileError{Kind: FailureTransientControlObservation, Revision: revision, Err: err}
		}
		return revision, critical(revision, err)
	}
	if current != selected.ApplicationPort {
		if err := adapter.Client.SetListenPort(ctx, selected.ApplicationPort); err != nil {
			return revision, critical(revision, err)
		}
	}
	if err := adapter.Client.VerifyListener(ctx, selected.ApplicationPort); err != nil {
		return revision, critical(revision, err)
	}
	acknowledgement := delivery.ApplicationAcknowledgement{APIVersion: delivery.AcknowledgementAPIVersion, PodUID: document.PodUID, LeaseIdentity: selected.Identity, Generation: selected.Generation, ApplicationPort: selected.ApplicationPort}
	payload, err := json.Marshal(acknowledgement)
	if err != nil {
		return revision, critical(revision, err)
	}
	endpoint := strings.TrimRight(adapter.LeaseEndpoint, "/") + "/" + url.PathEscape(selected.Identity) + "/ack"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return revision, critical(revision, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := adapter.http().Do(request)
	if err != nil {
		return revision, critical(revision, fmt.Errorf("acknowledge qBitTorrent lease generation: %w", err))
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return revision, critical(revision, fmt.Errorf("lease acknowledgement returned HTTP %d", response.StatusCode))
	}
	return revision, nil
}

func critical(revision LeaseRevision, err error) error {
	return &ReconcileError{Kind: FailureCritical, Revision: revision, Err: err}
}

func transientControlObservation(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func (adapter *Adapter) document(ctx context.Context) (delivery.Document, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(adapter.LeaseEndpoint, "/"), nil)
	if err != nil {
		return delivery.Document{}, err
	}
	response, err := adapter.http().Do(request)
	if err != nil {
		return delivery.Document{}, fmt.Errorf("read Pod-local lease document: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return delivery.Document{}, fmt.Errorf("pod-local lease document returned HTTP %d", response.StatusCode)
	}
	var document delivery.Document
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&document); err != nil {
		return delivery.Document{}, errors.New("decode Pod-local lease document")
	}
	if err := document.Validate(adapter.now()); err != nil {
		return delivery.Document{}, err
	}
	return document, nil
}

func (adapter *Adapter) http() *http.Client {
	if adapter.HTTP != nil {
		return adapter.HTTP
	}
	return &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (adapter *Adapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now().UTC()
	}
	return time.Now().UTC()
}
