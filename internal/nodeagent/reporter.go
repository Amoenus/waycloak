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
	"os"
	"strings"
	"time"
)

type Reporter struct {
	URL       string
	TokenFile string
	CAFile    string
	Client    *http.Client
}

func (r Reporter) Report(ctx context.Context, observations []Observation) error {
	if r.URL == "" || r.TokenFile == "" {
		return errors.New("observation relay URL and token file are required")
	}
	token, err := os.ReadFile(r.TokenFile)
	if err != nil {
		return fmt.Errorf("read projected observation token: %w", err)
	}
	body, err := json.Marshal(observations)
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
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("publish node observation: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("observation relay returned HTTP %d", response.StatusCode)
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
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, DisableKeepAlives: true}}, nil
}
