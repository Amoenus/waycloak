// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gluetun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

var (
	ErrTunnelUnhealthy = errors.New("gluetun tunnel health check failed")
	ErrDNSUnhealthy    = errors.New("gluetun DNS is not running")
)

type Engine struct {
	HealthURL  string
	ControlURL string
	Client     *http.Client
}

func New() *Engine {
	return &Engine{
		HealthURL:  "http://127.0.0.1:9999/",
		ControlURL: "http://127.0.0.1:8000",
		Client: &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
	}
}

func (engine *Engine) Observe(ctx context.Context) (provider.EngineObservation, error) {
	observation := provider.EngineObservation{}
	if err := engine.requireOK(ctx, engine.HealthURL); err != nil {
		return observation, fmt.Errorf("%w: %v", ErrTunnelUnhealthy, err)
	}
	observation.TunnelReady = true

	var dns struct {
		Status string `json:"status"`
	}
	if err := engine.getJSON(ctx, engine.ControlURL+"/v1/dns/status", &dns); err != nil || dns.Status != "running" {
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
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
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
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(target)
}

func (engine *Engine) client() *http.Client {
	if engine.Client != nil {
		return engine.Client
	}
	return &http.Client{Timeout: 3 * time.Second}
}
