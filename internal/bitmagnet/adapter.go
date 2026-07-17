// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package bitmagnet

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Amoenus/waycloak/internal/delivery"
)

type Adapter struct {
	LeaseEndpoint string
	LeaseName     string
	ConfigPath    string
	HTTP          *http.Client
	Now           func() time.Time
	ListenerReady func(uint16) (bool, error)
}

type Revision struct {
	Identity        string
	Generation      int64
	ApplicationPort uint16
	ConfigChanged   bool
}

// Stage writes the current provider-assigned port before Bitmagnet starts.
func (adapter *Adapter) Stage(ctx context.Context) (Revision, error) {
	document, record, err := adapter.currentRecord(ctx)
	if err != nil {
		return Revision{}, err
	}
	revision := Revision{Identity: record.Identity, Generation: record.Generation, ApplicationPort: record.ApplicationPort}
	changed, err := writeConfig(adapter.ConfigPath, record.ApplicationPort)
	revision.ConfigChanged = changed
	if err != nil {
		return revision, err
	}
	_ = document
	return revision, nil
}

// Reconcile stages the current port, observes Bitmagnet's UDP listener, and
// acknowledges only the exact generation that is actually bound.
func (adapter *Adapter) Reconcile(ctx context.Context) (Revision, error) {
	document, record, err := adapter.currentRecord(ctx)
	if err != nil {
		return Revision{}, err
	}
	revision := Revision{Identity: record.Identity, Generation: record.Generation, ApplicationPort: record.ApplicationPort}
	changed, err := writeConfig(adapter.ConfigPath, record.ApplicationPort)
	revision.ConfigChanged = changed
	if err != nil {
		return revision, err
	}
	listenerReady := adapter.ListenerReady
	if listenerReady == nil {
		listenerReady = UDPListenerReady
	}
	ready, err := listenerReady(record.ApplicationPort)
	if err != nil {
		return revision, fmt.Errorf("observe Bitmagnet DHT listener: %w", err)
	}
	if !ready {
		return revision, fmt.Errorf("bitmagnet DHT listener is not bound to UDP port %d", record.ApplicationPort)
	}
	ack := delivery.ApplicationAcknowledgement{APIVersion: delivery.AcknowledgementAPIVersion, PodUID: document.PodUID, LeaseIdentity: record.Identity, Generation: record.Generation, ApplicationPort: record.ApplicationPort}
	payload, err := json.Marshal(ack)
	if err != nil {
		return revision, err
	}
	endpoint := strings.TrimRight(adapter.LeaseEndpoint, "/") + "/" + url.PathEscape(record.Identity) + "/ack"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return revision, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := adapter.http().Do(request)
	if err != nil {
		return revision, fmt.Errorf("acknowledge Bitmagnet lease generation: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return revision, fmt.Errorf("lease acknowledgement returned HTTP %d", response.StatusCode)
	}
	return revision, nil
}

func (adapter *Adapter) currentRecord(ctx context.Context) (delivery.Document, delivery.Record, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(adapter.LeaseEndpoint, "/"), nil)
	if err != nil {
		return delivery.Document{}, delivery.Record{}, err
	}
	response, err := adapter.http().Do(request)
	if err != nil {
		return delivery.Document{}, delivery.Record{}, fmt.Errorf("read Pod-local lease document: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return delivery.Document{}, delivery.Record{}, fmt.Errorf("pod-local lease document returned HTTP %d", response.StatusCode)
	}
	var document delivery.Document
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&document); err != nil {
		return delivery.Document{}, delivery.Record{}, errors.New("decode Pod-local lease document")
	}
	if err := document.Validate(adapter.now()); err != nil {
		return delivery.Document{}, delivery.Record{}, err
	}
	var selected *delivery.Record
	for index := range document.Leases {
		record := &document.Leases[index]
		if record.ApplicationPortMode != delivery.ApplicationPortModeProviderAssigned || adapter.LeaseName != "" && record.Name != adapter.LeaseName {
			continue
		}
		if selected != nil {
			return delivery.Document{}, delivery.Record{}, errors.New("multiple provider-assigned leases match the Bitmagnet adapter")
		}
		selected = record
	}
	if selected == nil {
		return delivery.Document{}, delivery.Record{}, errors.New("a current provider-assigned lease is unavailable")
	}
	return document, *selected, nil
}

func writeConfig(path string, port uint16) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, errors.New("bitmagnet config path is required")
	}
	wanted := []byte(fmt.Sprintf("# Generated by the Waycloak Bitmagnet adapter.\ndht_server:\n  port: %d\n", port))
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, wanted) {
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read Bitmagnet config: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return false, fmt.Errorf("create Bitmagnet config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".waycloak-bitmagnet-*")
	if err != nil {
		return false, fmt.Errorf("create temporary Bitmagnet config: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if _, err := temporary.Write(wanted); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return false, fmt.Errorf("replace Bitmagnet config: %w", err)
	}
	return true, nil
}

// UDPListenerReady observes the shared Pod network namespace without needing
// a process-namespace share or a Linux capability.
func UDPListenerReady(port uint16) (bool, error) {
	wanted := strings.ToUpper(strconv.FormatUint(uint64(port), 16))
	wanted = strings.Repeat("0", 4-len(wanted)) + wanted
	for _, path := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		file, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) > 3 && strings.HasSuffix(fields[1], ":"+wanted) && fields[3] == "07" {
				_ = file.Close()
				return true, nil
			}
		}
		err = scanner.Err()
		_ = file.Close()
		if err != nil {
			return false, err
		}
	}
	return false, nil
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
