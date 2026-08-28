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
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

const (
	PortForwardCapabilityNative = "gluetun.waycloak.io/native-port-forward"
	controlResponseLimit        = int64(16 << 10)
	observationValidity         = 15 * time.Second
	observationRefresh          = 5 * time.Second
)

type PortForwardOptions struct {
	CapabilityName string
	ControlURL     string
	APIKeyFile     string
	Client         *http.Client
	Now            func() time.Time
}

// PortForwardCapabilityForConfig selects the currently qualified native
// Gluetun capability. Provider protocol packets and renewal remain entirely in
// Gluetun; this check only prevents an unqualified feature/configuration pair
// from reaching gateway mutation.
func PortForwardCapabilityForConfig(config map[string]string) (string, error) {
	providerName := strings.ToLower(strings.TrimSpace(config["VPN_SERVICE_PROVIDER"]))
	vpnType := strings.ToLower(strings.TrimSpace(config["VPN_TYPE"]))
	if providerName == "protonvpn" && vpnType == "openvpn" {
		return PortForwardCapabilityNative, nil
	}
	return "", fmt.Errorf("gluetun native configuration does not expose the qualified port-forward capability (provider=%q, vpnType=%q)", providerName, vpnType)
}

func NewPortForwardCapability(options PortForwardOptions) (provider.PortForwardCapability, error) {
	if options.CapabilityName != PortForwardCapabilityNative || options.APIKeyFile == "" {
		return nil, errors.New("exact authenticated Gluetun native port-forward capability is required")
	}
	controlURL := strings.TrimSuffix(options.ControlURL, "/")
	if controlURL == "" {
		controlURL = "http://127.0.0.1:8000"
	}
	parsed, err := url.Parse(controlURL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("gluetun control endpoint must be exact loopback HTTP")
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}}
	}
	return &NativePortForward{ControlURL: controlURL, APIKeyFile: options.APIKeyFile, Client: client, Now: options.Now}, nil
}

type NativePortForward struct {
	ControlURL string
	APIKeyFile string
	Client     *http.Client
	Now        func() time.Time
}

func (*NativePortForward) ObserveCapabilities(context.Context) (provider.PortForwardCapabilities, error) {
	return provider.PortForwardCapabilities{Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}, MaxLeases: 1,
		SharedPort: true, SupportsRequestedPort: false, MinimumLeaseDuration: observationValidity}, nil
}

func (c *NativePortForward) EnsureLease(ctx context.Context, request provider.PortForwardLeaseRequest) (provider.PortForwardLeaseObservation, error) {
	if request.Identity == "" || request.InternalPort == 0 || !supportedNativeProtocols(request.Protocols) {
		return provider.PortForwardLeaseObservation{}, errors.New("gluetun native port-forward request is invalid")
	}
	var forwarded struct {
		Port  uint16   `json:"port"`
		Ports []uint16 `json:"ports"`
	}
	if err := c.getJSON(ctx, "/v1/portforward", &forwarded); err != nil {
		return provider.PortForwardLeaseObservation{}, fmt.Errorf("observe gluetun forwarded port: %w", err)
	}
	if len(forwarded.Ports) != 1 || forwarded.Ports[0] == 0 || forwarded.Port != 0 && forwarded.Port != forwarded.Ports[0] {
		return provider.PortForwardLeaseObservation{}, errors.New("gluetun reports no exact shared forwarded port")
	}
	var publicIP struct {
		PublicIP string `json:"public_ip"`
	}
	if err := c.getJSON(ctx, "/v1/publicip/ip", &publicIP); err != nil {
		return provider.PortForwardLeaseObservation{}, fmt.Errorf("observe gluetun public address: %w", err)
	}
	address, err := netip.ParseAddr(strings.TrimSpace(publicIP.PublicIP))
	if err != nil || !address.IsGlobalUnicast() {
		return provider.PortForwardLeaseObservation{}, errors.New("gluetun public address observation is invalid")
	}
	now := c.now()
	return provider.PortForwardLeaseObservation{PublicAddress: address, PublicPort: forwarded.Ports[0], IssuedAt: now,
		RenewAfter: now.Add(observationRefresh), ExpiresAt: now.Add(observationValidity)}, nil
}

func supportedNativeProtocols(protocols []provider.PortForwardProtocol) bool {
	if len(protocols) == 0 || len(protocols) > 2 {
		return false
	}
	seen := make(map[provider.PortForwardProtocol]struct{}, len(protocols))
	for _, protocol := range protocols {
		if protocol != provider.ProtocolTCP && protocol != provider.ProtocolUDP {
			return false
		}
		if _, exists := seen[protocol]; exists {
			return false
		}
		seen[protocol] = struct{}{}
	}
	return true
}

func (*NativePortForward) ReleaseLease(context.Context, provider.PortForwardLeaseRequest) error {
	// Gluetun owns provider release as part of its VPN/port-forward service
	// lifecycle. Waycloak withdraws its translation and delivery immediately;
	// it never sends provider protocol packets or disables engine-global state.
	return nil
}

func (c *NativePortForward) getJSON(ctx context.Context, path string, output any) error {
	if c == nil || c.Client == nil || c.ControlURL == "" || c.APIKeyFile == "" {
		return errors.New("gluetun native client is not configured")
	}
	key, err := readControlAPIKey(c.APIKeyFile)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ControlURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-API-Key", key)
	response, err := c.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, controlResponseLimit))
		return fmt.Errorf("gluetun control returned status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, controlResponseLimit))
	if err := decoder.Decode(output); err != nil {
		return errors.New("gluetun control response is invalid")
	}
	return nil
}

func readControlAPIKey(path string) (string, error) {
	apiKey, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("read gluetun control identity")
	}
	key := strings.TrimSpace(string(apiKey))
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		return "", errors.New("gluetun control identity is invalid")
	}
	return key, nil
}

func (c *NativePortForward) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

var _ provider.PortForwardCapability = (*NativePortForward)(nil)
