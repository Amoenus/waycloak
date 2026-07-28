// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrentadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestClientAppliesObservesProbesAndReannouncesExactPod(t *testing.T) {
	listenPort := uint16(6881)
	reannounces := 0
	probeEndpoint := netip.AddrPort{}
	client := &Client{Port: 8443, ServerName: "qbittorrent.apps.test", Username: "user", Password: "password",
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme != "https" || request.URL.Host != "10.42.0.10:8443" || request.Host != "qbittorrent.apps.test" {
				t.Fatalf("qBittorrent request identity = %s host=%q", request.URL, request.Host)
			}
			switch request.URL.Path {
			case "/api/v2/auth/login":
				body := parseForm(t, request)
				if body.Get("username") != "user" || body.Get("password") != "password" {
					t.Fatal("qBittorrent application credentials were not sent exactly")
				}
				return textResponse("Ok.", "SID=session"), nil
			case "/api/v2/app/setPreferences":
				if !strings.Contains(request.Header.Get("Cookie"), "SID=session") {
					t.Fatal("authenticated qBittorrent session was not retained")
				}
				var preferences struct {
					ListenPort uint16 `json:"listen_port"`
					RandomPort bool   `json:"random_port"`
					UPnP       bool   `json:"upnp"`
				}
				if err := json.Unmarshal([]byte(parseForm(t, request).Get("json")), &preferences); err != nil || preferences.RandomPort || preferences.UPnP {
					t.Fatalf("qBittorrent preferences = %#v, %v", preferences, err)
				}
				listenPort = preferences.ListenPort
				return textResponse("", ""), nil
			case "/api/v2/app/preferences":
				return jsonResponse(map[string]any{"listen_port": listenPort, "random_port": false, "upnp": false}), nil
			case "/api/v2/torrents/reannounce":
				if parseForm(t, request).Get("hashes") != "all" {
					t.Fatal("qBittorrent reannounce did not cover all active torrents")
				}
				reannounces++
				return textResponse("", ""), nil
			default:
				t.Fatalf("unexpected qBittorrent request %s", request.URL.Path)
				return nil, nil
			}
		}), Probe: func(_ context.Context, endpoint netip.AddrPort) error { probeEndpoint = endpoint; return nil }}
	if err := client.Configure(context.Background(), netip.MustParseAddr("10.42.0.10"), 42000, true); err != nil {
		t.Fatal(err)
	}
	if listenPort != 42000 || probeEndpoint != netip.MustParseAddrPort("10.42.0.10:42000") || reannounces != 1 {
		t.Fatalf("qBittorrent result port=%d probe=%s reannounces=%d", listenPort, probeEndpoint, reannounces)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func parseForm(t *testing.T, request *http.Request) url.Values {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func textResponse(body, cookie string) *http.Response {
	header := http.Header{"Content-Type": []string{"text/plain"}, "Content-Length": []string{strconv.Itoa(len(body))}}
	if cookie != "" {
		header.Set("Set-Cookie", cookie+"; Path=/; HttpOnly")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func jsonResponse(value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
