// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package nodeagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const ObservationPublicationTimeout = 9 * time.Second

type Reporter struct {
	URL       string
	TokenFile string
	CAFile    string
	Client    *http.Client
}

func (r Reporter) Report(ctx context.Context, report Report) error {
	if r.URL == "" || r.TokenFile == "" {
		return errors.New("observation relay URL and token file are required")
	}
	token, err := os.ReadFile(r.TokenFile)
	if err != nil {
		return fmt.Errorf("read projected observation token: %w", err)
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Content-Type", "application/json")
	client := r.Client
	if client == nil {
		client, err = r.client()
		if err != nil {
			return err
		}
	}
	started := time.Now()
	var phase atomic.Value
	phase.Store("connect")
	trace := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { phase.Store("dns_lookup") },
		ConnectStart:      func(_, _ string) { phase.Store("connect") },
		TLSHandshakeStart: func() { phase.Store("tls_handshake") },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				phase.Store("request_write")
				return
			}
			phase.Store("response_headers")
		},
		GotFirstResponseByte: func() {
			phase.Store("response_body")
		},
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("publish node observation phase=%s elapsed=%s: %w", phase.Load(), time.Since(started).Round(time.Millisecond), err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("observation relay returned HTTP %d phase=response_status elapsed=%s", response.StatusCode, time.Since(started).Round(time.Millisecond))
	}
	return nil
}

func (r Reporter) client() (*http.Client, error) {
	if r.CAFile == "" {
		return nil, errors.New("observation relay CA file is required")
	}
	ca, err := os.ReadFile(r.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read observation relay CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("observation relay CA contains no certificate")
	}
	return &http.Client{Timeout: ObservationPublicationTimeout, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, DisableKeepAlives: true}}, nil
}
