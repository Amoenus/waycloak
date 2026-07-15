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
	request, err := client.request(ctx, http.MethodGet, "/api/v2/app/preferences", nil)
	if err != nil {
		return 0, err
	}
	response, err := client.http().Do(request)
	if err != nil {
		return 0, fmt.Errorf("query qBitTorrent preferences: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return 0, fmt.Errorf("qBitTorrent preferences returned HTTP %d", response.StatusCode)
	}
	var preferences struct {
		ListenPort int `json:"listen_port"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&preferences); err != nil || preferences.ListenPort < 1 || preferences.ListenPort > 65535 {
		return 0, errors.New("qBitTorrent returned an invalid listen port")
	}
	return uint16(preferences.ListenPort), nil
}

func (client *Client) SetListenPort(ctx context.Context, port uint16) error {
	if port == 0 {
		return errors.New("qBitTorrent listen port is required")
	}
	payload, err := json.Marshal(map[string]int{"listen_port": int(port)})
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
		return fmt.Errorf("update qBitTorrent listen port: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("qBitTorrent preference update returned HTTP %d", response.StatusCode)
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

func (client *Client) VerifyListener(ctx context.Context, port uint16) error {
	if port == 0 {
		return errors.New("qBitTorrent listen port is required")
	}
	dialer := &net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
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
