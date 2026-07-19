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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

type Preferences struct {
	ListenPort       int    `json:"listen_port"`
	DHTEnabled       bool   `json:"dht"`
	NetworkInterface string `json:"current_network_interface"`
	InterfaceAddress string `json:"current_interface_address"`
	AnnounceAddress  string `json:"announce_ip"`
}

func (client *Client) Validate() error {
	endpoint, err := url.Parse(client.BaseURL)
	if err != nil || endpoint.Scheme != "http" || endpoint.Path != "" && endpoint.Path != "/" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("qBitTorrent endpoint must be a plain HTTP loopback URL")
	}
	host := endpoint.Hostname()
	address := net.ParseIP(host)
	if host != "localhost" && (address == nil || !address.IsLoopback()) {
		return errors.New("qBitTorrent endpoint must use loopback")
	}
	if strings.TrimSpace(client.APIKey) == "" {
		return errors.New("qBitTorrent API key is required")
	}
	return nil
}

func (client *Client) ListenPort(ctx context.Context) (uint16, error) {
	preferences, err := client.Preferences(ctx)
	if err != nil {
		return 0, err
	}
	return uint16(preferences.ListenPort), nil
}

func (client *Client) Preferences(ctx context.Context) (Preferences, error) {
	request, err := client.request(ctx, http.MethodGet, "/api/v2/app/preferences", nil)
	if err != nil {
		return Preferences{}, err
	}
	response, err := client.http().Do(request)
	if err != nil {
		return Preferences{}, fmt.Errorf("query qBitTorrent preferences: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Preferences{}, fmt.Errorf("qBitTorrent preferences returned HTTP %d", response.StatusCode)
	}
	var preferences Preferences
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&preferences); err != nil || preferences.ListenPort < 1 || preferences.ListenPort > 65535 {
		return Preferences{}, errors.New("qBitTorrent returned invalid preferences")
	}
	return preferences, nil
}

func (client *Client) SetListenPort(ctx context.Context, port uint16) error {
	if port == 0 {
		return errors.New("qBitTorrent listen port is required")
	}
	if err := client.setPreferences(ctx, map[string]any{"listen_port": int(port)}); err != nil {
		return fmt.Errorf("update qBitTorrent listen port: %w", err)
	}
	observed, err := client.ListenPort(ctx)
	if err != nil {
		return err
	}
	if observed != port {
		return fmt.Errorf("qBitTorrent listen port is %s after update", strconv.Itoa(int(observed)))
	}
	return nil
}

func (client *Client) SetNetworkBinding(ctx context.Context, interfaceName, address string) error {
	if strings.TrimSpace(interfaceName) == "" || net.ParseIP(strings.TrimSpace(address)) == nil {
		return errors.New("qBitTorrent network binding is invalid")
	}
	if err := client.setPreferences(ctx, map[string]any{"current_network_interface": interfaceName, "current_interface_address": address}); err != nil {
		return fmt.Errorf("update qBitTorrent network binding: %w", err)
	}
	observed, err := client.Preferences(ctx)
	if err != nil {
		return err
	}
	if observed.NetworkInterface != interfaceName || observed.InterfaceAddress != address {
		return errors.New("qBitTorrent did not apply the Waycloak network binding")
	}
	return nil
}

func (client *Client) SetAnnounceAddress(ctx context.Context, address string) error {
	publicAddress, err := netip.ParseAddr(strings.TrimSpace(address))
	if err != nil || !publicAddress.Is4() || !publicAddress.IsGlobalUnicast() {
		return errors.New("qBitTorrent announce address is invalid")
	}
	if err := client.setPreferences(ctx, map[string]any{"announce_ip": publicAddress.String()}); err != nil {
		return fmt.Errorf("update qBitTorrent announce address: %w", err)
	}
	observed, err := client.Preferences(ctx)
	if err != nil {
		return err
	}
	if observed.AnnounceAddress != publicAddress.String() {
		return errors.New("qBitTorrent did not apply the provider public address")
	}
	return nil
}

func (client *Client) RestartDHT(ctx context.Context) error {
	if err := client.setPreferences(ctx, map[string]any{"dht": false}); err != nil {
		return fmt.Errorf("stop qBitTorrent DHT: %w", err)
	}
	if err := client.setPreferences(ctx, map[string]any{"dht": true}); err != nil {
		return fmt.Errorf("restart qBitTorrent DHT: %w", err)
	}
	observed, err := client.Preferences(ctx)
	if err != nil {
		return err
	}
	if !observed.DHTEnabled {
		return errors.New("qBitTorrent DHT remained disabled after restart")
	}
	return nil
}

func (client *Client) ReannounceAll(ctx context.Context) error {
	form := url.Values{"hashes": []string{"all"}}
	request, err := client.request(ctx, http.MethodPost, "/api/v2/torrents/reannounce", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.http().Do(request)
	if err != nil {
		return fmt.Errorf("reannounce qBitTorrent torrents: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("qBitTorrent reannounce returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (client *Client) setPreferences(ctx context.Context, values map[string]any) error {
	payload, err := json.Marshal(values)
	if err != nil {
		return err
	}
	form := url.Values{"json": []string{string(payload)}}
	request, err := client.request(ctx, http.MethodPost, "/api/v2/app/setPreferences", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.http().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("qBitTorrent preference update returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (client *Client) VerifyListener(ctx context.Context, address string, port uint16) error {
	if port == 0 {
		return errors.New("qBitTorrent listen port is required")
	}
	if net.ParseIP(strings.TrimSpace(address)) == nil {
		return errors.New("qBitTorrent listener address is invalid")
	}
	dialer := &net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(int(port))))
	if err != nil {
		return fmt.Errorf("qBitTorrent is not accepting connections on listen port %d: %w", port, err)
	}
	if err := connection.Close(); err != nil {
		return fmt.Errorf("close qBitTorrent listener probe: %w", err)
	}
	return nil
}

func (client *Client) request(ctx context.Context, method, path string, body *strings.Reader) (*http.Request, error) {
	if err := client.Validate(); err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = body
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(client.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(client.APIKey))
	return request, nil
}

func (client *Client) http() *http.Client {
	if client.HTTP != nil {
		return client.HTTP
	}
	return &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
