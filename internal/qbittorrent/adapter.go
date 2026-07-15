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

func (adapter *Adapter) Reconcile(ctx context.Context) error {
	document, err := adapter.document(ctx)
	if err != nil {
		return err
	}
	var selected *delivery.Record
	for i := range document.Leases {
		record := &document.Leases[i]
		if record.ApplicationPortMode != delivery.ApplicationPortModeProviderAssigned || adapter.LeaseName != "" && record.Name != adapter.LeaseName {
			continue
		}
		if selected != nil {
			return errors.New("multiple provider-assigned leases match the qBitTorrent adapter")
		}
		selected = record
	}
	if selected == nil || !adapter.now().Before(selected.ExpiresAt) {
		return errors.New("a current provider-assigned lease is unavailable")
	}
	if adapter.Client == nil {
		return errors.New("qBitTorrent client is required")
	}
	current, err := adapter.Client.ListenPort(ctx)
	if err != nil {
		return err
	}
	if current != selected.ApplicationPort {
		if err := adapter.Client.SetListenPort(ctx, selected.ApplicationPort); err != nil {
			return err
		}
	}
	if err := adapter.Client.VerifyListener(ctx, selected.ApplicationPort); err != nil {
		return err
	}
	acknowledgement := delivery.ApplicationAcknowledgement{Generation: selected.Generation, ApplicationPort: selected.ApplicationPort}
	payload, err := json.Marshal(acknowledgement)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(adapter.LeaseEndpoint, "/") + "/" + url.PathEscape(selected.Identity) + "/ack"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := adapter.http().Do(request)
	if err != nil {
		return fmt.Errorf("acknowledge qBitTorrent lease generation: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("lease acknowledgement returned HTTP %d", response.StatusCode)
	}
	return nil
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
