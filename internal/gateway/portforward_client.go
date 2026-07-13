// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

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
)

var ErrPortForwardObservationNotFound = errors.New("port-forward lease observation not found")

type PortForwardObserver interface {
	ObserveLease(context.Context, string, string) (PortForwardObservation, error)
}

type HTTPPortForwardObserver struct {
	Client *http.Client
	Port   int
}

func (observer *HTTPPortForwardObserver) ObserveLease(ctx context.Context, podIP, identity string) (PortForwardObservation, error) {
	address, err := netip.ParseAddr(podIP)
	if err != nil || identity == "" {
		return PortForwardObservation{}, errors.New("invalid gateway observation target")
	}
	port := observer.Port
	if port == 0 {
		port = HealthPort
	}
	endpoint := "http://" + net.JoinHostPort(address.String(), strconv.Itoa(port)) + "/v1/port-forward/leases/" + url.PathEscape(identity)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return PortForwardObservation{}, err
	}
	response, err := observer.client().Do(request)
	if err != nil {
		return PortForwardObservation{}, fmt.Errorf("read gateway port-forward observations: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return PortForwardObservation{}, ErrPortForwardObservationNotFound
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return PortForwardObservation{}, fmt.Errorf("gateway port-forward observation returned HTTP %d", response.StatusCode)
	}
	var document PortForwardObservationDocument
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64*1024))
	if err := decoder.Decode(&document); err != nil {
		return PortForwardObservation{}, fmt.Errorf("decode gateway port-forward observations: %w", err)
	}
	if document.APIVersion != PortForwardObservationAPIVersion {
		return PortForwardObservation{}, fmt.Errorf("unsupported gateway port-forward observation version %q", document.APIVersion)
	}
	if document.Lease.Identity != identity {
		return PortForwardObservation{}, ErrPortForwardObservationNotFound
	}
	return document.Lease, nil
}

func (observer *HTTPPortForwardObserver) client() *http.Client {
	if observer.Client != nil {
		return observer.Client
	}
	return &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
