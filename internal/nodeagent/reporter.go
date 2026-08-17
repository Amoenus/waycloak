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
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const (
	ObservationPublicationTimeout = 9 * time.Second
	observationDialAttemptTimeout = time.Second
	observationDialRetryDelay     = 100 * time.Millisecond
)

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
	dialer := &net.Dialer{}
	return &http.Client{Timeout: ObservationPublicationTimeout, Transport: &http.Transport{
		Proxy:             nil,
		DialContext:       resolvingObservationDialContext(net.DefaultResolver.LookupIPAddr, dialer.DialContext),
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots},
		DisableKeepAlives: true,
	}}, nil
}

type observationDialAttempt func(context.Context, string, string) (net.Conn, error)
type observationLookupAttempt func(context.Context, string) ([]net.IPAddr, error)

// resolvingObservationDialContext retries only transient DNS resolution. A
// permanent name error or TCP connection failure returns immediately, and a
// POST that reached request writing is never replayed. Every lookup remains
// bounded by the existing publication context, so genuine DNS loss withdraws
// node authority at the same fail-closed deadline.
func resolvingObservationDialContext(lookup observationLookupAttempt, dial observationDialAttempt) observationDialAttempt {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse observation relay address: %w", err)
		}
		if net.ParseIP(host) != nil {
			return dial(ctx, network, address)
		}

		attempts := 0
		var lastErr error
		for {
			if err := ctx.Err(); err != nil {
				if lastErr == nil {
					return nil, fmt.Errorf("resolve observation relay before first attempt: %w", err)
				}
				return nil, fmt.Errorf("resolve observation relay after %d attempts: %w", attempts, lastErr)
			}

			attempts++
			attemptCtx, cancel := context.WithTimeout(ctx, observationDialAttemptTimeout)
			addresses, err := lookup(attemptCtx, host)
			cancel()
			if err == nil {
				if len(addresses) == 0 {
					return nil, errors.New("observation relay DNS lookup returned no addresses")
				}
				var dialErr error
				for _, resolved := range addresses {
					connection, currentErr := dial(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
					if currentErr == nil {
						return connection, nil
					}
					dialErr = currentErr
					if ctx.Err() != nil {
						break
					}
				}
				return nil, fmt.Errorf("connect observation relay after DNS resolution: %w", dialErr)
			}
			lastErr = err
			if !retryableObservationLookupError(ctx, err) {
				return nil, fmt.Errorf("resolve observation relay after %d attempts: %w", attempts, err)
			}

			timer := time.NewTimer(observationDialRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("resolve observation relay after %d attempts: %w", attempts, lastErr)
			case <-timer.C:
			}
		}
	}
}

func retryableObservationLookupError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && (dnsErr.IsTimeout || dnsErr.IsTemporary)
}
