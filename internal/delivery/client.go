// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/Amoenus/waycloak/internal/contract"
)

type Observer interface {
	ObserveDelivery(context.Context, string, string) (Observation, error)
}

type HTTPObserver struct {
	Client *http.Client
	Port   int
}

func (observer *HTTPObserver) ObserveDelivery(ctx context.Context, podIP, identity string) (Observation, error) {
	address, err := netip.ParseAddr(podIP)
	if err != nil || identity == "" {
		return Observation{}, errors.New("invalid delivery observation target")
	}
	port := observer.Port
	if port == 0 {
		port = contract.AgentHealthPort
	}
	endpoint := "http://" + net.JoinHostPort(address.String(), strconv.Itoa(port)) + "/v1/port-forward/deliveries/" + url.PathEscape(identity)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Observation{}, err
	}
	response, err := observer.client().Do(request)
	if err != nil {
		return Observation{}, fmt.Errorf("read Pod port-forward delivery observation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Observation{}, ErrRecordNotFound
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Observation{}, fmt.Errorf("pod delivery observation returned HTTP %d", response.StatusCode)
	}
	var observation Observation
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&observation); err != nil {
		return Observation{}, fmt.Errorf("decode Pod delivery observation: %w", err)
	}
	if observation.APIVersion != APIVersion || observation.Identity != identity {
		return Observation{}, ErrRecordNotFound
	}
	return observation, nil
}

func (observer *HTTPObserver) client() *http.Client {
	if observer.Client != nil {
		return observer.Client
	}
	return &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
