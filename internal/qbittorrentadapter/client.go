// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrentadapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const responseLimit = int64(64 << 10)

type Client struct {
	Transport     http.RoundTripper
	Port          uint16
	ServerName    string
	Username      string
	Password      string
	Probe         func(context.Context, netip.AddrPort) error
	ProtocolProbe func(context.Context, string, netip.AddrPort) error
}

func NewClient(caFile, serverName, usernameFile, passwordFile string, port uint16) (*Client, error) {
	if caFile == "" || serverName == "" || usernameFile == "" || passwordFile == "" || port == 0 {
		return nil, errors.New("qBittorrent TLS identity, credential files, and port are required")
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read qBittorrent CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("qBittorrent CA bundle contains no certificate")
	}
	username, err := readCredential(usernameFile)
	if err != nil {
		return nil, fmt.Errorf("read qBittorrent username: %w", err)
	}
	password, err := readCredential(passwordFile)
	if err != nil {
		return nil, fmt.Errorf("read qBittorrent password: %w", err)
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: serverName},
		DisableCompression: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second}
	return &Client{Transport: transport, Port: port, ServerName: serverName, Username: username, Password: password}, nil
}

func (c *Client) Apply(ctx context.Context, address netip.Addr, port uint16, reannounce bool) error {
	httpClient, origin, err := c.session(ctx, address, port)
	if err != nil {
		return err
	}
	// A successful login response is not sufficient proof of authorization:
	// qBittorrent 5.2 may return 204 for a response without content. Require a
	// protected read before changing any application state.
	if _, err := c.preferences(ctx, httpClient, origin); err != nil {
		return fmt.Errorf("verify qBittorrent authentication: %w", err)
	}
	preferences := map[string]any{"listen_port": port, "random_port": false, "upnp": false}
	preferenceJSON, err := json.Marshal(preferences)
	if err != nil {
		return err
	}
	if err := c.postForm(ctx, httpClient, origin, "/api/v2/app/setPreferences", url.Values{"json": {string(preferenceJSON)}}, ""); err != nil {
		return fmt.Errorf("set qBittorrent listener: %w", err)
	}
	if reannounce {
		if err := c.postForm(ctx, httpClient, origin, "/api/v2/torrents/reannounce", url.Values{"hashes": {"all"}}, ""); err != nil {
			return fmt.Errorf("reannounce qBittorrent torrents: %w", err)
		}
	}
	return nil
}

// Configure remains as a source-compatible convenience for embedders. The
// adapter handler uses the separate Apply and Observe responsibilities.
func (c *Client) Configure(ctx context.Context, address netip.Addr, port uint16, reannounce bool) error {
	if err := c.Apply(ctx, address, port, reannounce); err != nil {
		return err
	}
	return c.Observe(ctx, address, port, []string{"tcp", "udp"})
}

func (c *Client) Observe(ctx context.Context, address netip.Addr, port uint16, protocols []string) error {
	httpClient, origin, err := c.session(ctx, address, port)
	if err != nil {
		return err
	}
	observed, err := c.preferences(ctx, httpClient, origin)
	if err != nil {
		return err
	}
	if observed.ListenPort != port || observed.RandomPort || observed.UPnP {
		return errors.New("qBittorrent listener observation does not match the lease")
	}
	endpoint := netip.AddrPortFrom(address, port)
	for _, protocol := range protocols {
		if protocol != "tcp" && protocol != "udp" {
			return errors.New("qBittorrent listener protocol is invalid")
		}
		var probeErr error
		if c.ProtocolProbe != nil {
			probeErr = c.ProtocolProbe(ctx, protocol, endpoint)
		} else if c.Probe != nil {
			probeErr = c.Probe(ctx, endpoint)
		} else {
			probeErr = probeListener(ctx, protocol, endpoint)
		}
		if probeErr != nil {
			return fmt.Errorf("observe qBittorrent %s listener: %w", protocol, probeErr)
		}
	}
	return nil
}

func (c *Client) session(ctx context.Context, address netip.Addr, port uint16) (*http.Client, *url.URL, error) {
	if c == nil || c.Transport == nil || c.Port == 0 || c.ServerName == "" || c.Username == "" || c.Password == "" || !address.Is4() || port == 0 {
		return nil, nil, errors.New("exact qBittorrent application identity is required")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, nil, err
	}
	httpClient := &http.Client{Transport: c.Transport, Jar: jar, Timeout: 10 * time.Second}
	origin := &url.URL{Scheme: "https", Host: net.JoinHostPort(address.String(), strconv.Itoa(int(c.Port)))}
	if err := c.authenticate(ctx, httpClient, origin); err != nil {
		return nil, nil, fmt.Errorf("authenticate qBittorrent: %w", err)
	}
	return httpClient, origin, nil
}

func (c *Client) authenticate(ctx context.Context, httpClient *http.Client, origin *url.URL) error {
	requestURL := *origin
	requestURL.Path = "/api/v2/auth/login"
	values := url.Values{"username": {c.Username}, "password": {c.Password}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	request.Host = c.ServerName
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := boundedBody(response.Body)
	if err != nil {
		return err
	}
	switch response.StatusCode {
	case http.StatusOK:
		if strings.TrimSpace(string(body)) != "Ok." {
			return errors.New("qBittorrent login response is invalid")
		}
	case http.StatusNoContent:
		if len(body) != 0 {
			return errors.New("qBittorrent no-content login response is invalid")
		}
	default:
		return fmt.Errorf("qBittorrent login returned status %d", response.StatusCode)
	}
	return nil
}

type observedPreferences struct {
	ListenPort uint16 `json:"listen_port"`
	RandomPort bool   `json:"random_port"`
	UPnP       bool   `json:"upnp"`
}

func (c *Client) preferences(ctx context.Context, httpClient *http.Client, origin *url.URL) (observedPreferences, error) {
	requestURL := *origin
	requestURL.Path = "/api/v2/app/preferences"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return observedPreferences{}, err
	}
	request.Host = c.ServerName
	response, err := httpClient.Do(request)
	if err != nil {
		return observedPreferences{}, fmt.Errorf("observe qBittorrent preferences: %w", err)
	}
	defer response.Body.Close()
	body, err := boundedBody(response.Body)
	if err != nil {
		return observedPreferences{}, err
	}
	if response.StatusCode != http.StatusOK {
		return observedPreferences{}, fmt.Errorf("qBittorrent preferences returned status %d", response.StatusCode)
	}
	var preferences observedPreferences
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&preferences); err != nil {
		return observedPreferences{}, errors.New("qBittorrent preferences response is invalid")
	}
	return preferences, nil
}

func (c *Client) postForm(ctx context.Context, httpClient *http.Client, origin *url.URL, path string, values url.Values, wantBody string) error {
	requestURL := *origin
	requestURL.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	request.Host = c.ServerName
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := boundedBody(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("qBittorrent request returned status %d", response.StatusCode)
	}
	if response.StatusCode == http.StatusNoContent && len(body) != 0 {
		return errors.New("qBittorrent no-content response is invalid")
	}
	if wantBody != "" && strings.TrimSpace(string(body)) != wantBody {
		return errors.New("qBittorrent response body is invalid")
	}
	return nil
}

func boundedBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, responseLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > responseLimit {
		return nil, errors.New("qBittorrent response exceeds size limit")
	}
	return body, nil
}

func probeListener(ctx context.Context, protocol string, endpoint netip.AddrPort) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, err := dialer.DialContext(ctx, protocol, endpoint.String())
	if err != nil {
		return err
	}
	if protocol == "udp" {
		deadline := time.Now().Add(2 * time.Second)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		if err := connection.SetDeadline(deadline); err != nil {
			_ = connection.Close()
			return err
		}
		// BEP 5 DHT ping. A matching transaction response proves that the UDP
		// socket belongs to a live BitTorrent listener; UDP connect alone does not.
		request := []byte("d1:ad2:id20:waycloak-listener-01e1:q4:ping1:t2:wc1:y1:qe")
		if _, err := connection.Write(request); err != nil {
			_ = connection.Close()
			return err
		}
		response := make([]byte, 2048)
		count, err := connection.Read(response)
		if err != nil {
			_ = connection.Close()
			return err
		}
		body := string(response[:count])
		if !strings.Contains(body, "1:t2:wc") || !strings.Contains(body, "1:y1:r") {
			_ = connection.Close()
			return errors.New("qBittorrent DHT ping response is invalid")
		}
	}
	return connection.Close()
}

func readCredential(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(contents))
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("credential file contains no single value")
	}
	return value, nil
}
