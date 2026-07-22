// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

var (
	ErrTunnelUnhealthy = errors.New("gluetun tunnel health check failed")
	ErrDNSUnhealthy    = errors.New("gluetun DNS is not running")
)

type httpStatusError struct{ code int }

func (err *httpStatusError) Error() string { return fmt.Sprintf("unexpected HTTP status %d", err.code) }

type Engine struct {
	HealthURL  string
	ControlURL string
	Client     *http.Client
	Logger     *slog.Logger
}

func New() *Engine {
	return &Engine{
		HealthURL:  "http://127.0.0.1:9999/",
		ControlURL: "http://127.0.0.1:8000",
		Client: &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{
				Proxy:               nil,
				MaxIdleConns:        4,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

func (engine *Engine) Observe(ctx context.Context) (provider.EngineObservation, error) {
	observation := provider.EngineObservation{}
	if err := engine.verifyTransport(ctx, "tunnel-health", func() error { return engine.requireOK(ctx, engine.HealthURL) }); err != nil {
		return observation, fmt.Errorf("%w: %v", ErrTunnelUnhealthy, err)
	}
	observation.TunnelReady = true

	var dns struct {
		Status string `json:"status"`
	}
	if err := engine.verifyTransport(ctx, "dns-status", func() error {
		dns.Status = ""
		return engine.getJSON(ctx, engine.ControlURL+"/v1/dns/status", &dns)
	}); err != nil || dns.Status != "running" {
		return observation, ErrDNSUnhealthy
	}
	observation.DNSReady = true

	var publicIP struct {
		PublicIP string `json:"public_ip"`
	}
	if err := engine.getJSON(ctx, engine.ControlURL+"/v1/publicip/ip", &publicIP); err == nil {
		address, parseErr := netip.ParseAddr(strings.TrimSpace(publicIP.PublicIP))
		if parseErr == nil && address.IsGlobalUnicast() {
			observation.PublicIP = address
		}
	}
	return observation, nil
}

// verifyTransport distinguishes one failed HTTP exchange from an observed
// unhealthy engine. EOF, truncated responses, and connection resets can occur
// when the loopback peer closes a connection during an exchange. A second GET
// after discarding idle connections is an independent observation. HTTP error
// statuses, timeouts, and context cancellation are authoritative and are not
// retried, so a genuinely unhealthy engine still withdraws readiness promptly.
func (engine *Engine) verifyTransport(ctx context.Context, component string, observe func() error) error {
	firstErr := observe()
	if firstErr == nil || !transientTransportError(firstErr) {
		return firstErr
	}
	engine.client().CloseIdleConnections()
	secondErr := observe()
	engine.logger().Warn("gluetun transport verification completed",
		"event", "gluetun_transport_verification",
		"component", component,
		"recovered", secondErr == nil,
		"first_error", firstErr.Error(),
		"second_error", errorString(secondErr),
	)
	return secondErr
}

func transientTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var requestErr *url.Error
	if !errors.As(err, &requestErr) {
		return false
	}
	var networkErr interface{ Timeout() bool }
	return !errors.As(requestErr.Err, &networkErr) || !networkErr.Timeout()
}

func (engine *Engine) requireOK(ctx context.Context, url string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := engine.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return &httpStatusError{code: response.StatusCode}
	}
	return nil
}

func (engine *Engine) getJSON(ctx context.Context, url string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := engine.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return &httpStatusError{code: response.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(target)
}

func (engine *Engine) client() *http.Client {
	if engine.Client != nil {
		return engine.Client
	}
	return &http.Client{Timeout: 3 * time.Second}
}

func (engine *Engine) logger() *slog.Logger {
	if engine.Logger != nil {
		return engine.Logger
	}
	return slog.Default()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
