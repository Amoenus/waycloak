// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrentadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestUDPListenerProbeRequiresMatchingDHTResponse(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		request := make([]byte, 2048)
		count, peer, err := listener.ReadFromUDP(request)
		if err != nil {
			done <- err
			return
		}
		if !strings.Contains(string(request[:count]), "1:t2:wc") || !strings.Contains(string(request[:count]), "1:q4:ping") {
			done <- errors.New("DHT request did not contain the bound ping transaction")
			return
		}
		_, err = listener.WriteToUDP([]byte("d1:rd2:id20:qbittorrent-node-001e1:t2:wc1:y1:re"), peer)
		done <- err
	}()
	endpoint := listener.LocalAddr().(*net.UDPAddr).AddrPort()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probeListener(ctx, "udp", endpoint); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientAppliesObservesProbesAndReannouncesExactPod(t *testing.T) {
	listenPort := uint16(6881)
	reannounces := 0
	probeEndpoint := netip.AddrPort{}
	probedProtocols := []string{}
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
		}), ProtocolProbe: func(_ context.Context, protocol string, endpoint netip.AddrPort) error {
			probedProtocols = append(probedProtocols, protocol)
			probeEndpoint = endpoint
			return nil
		}}
	if err := client.Configure(context.Background(), netip.MustParseAddr("10.42.0.10"), 42000, true); err != nil {
		t.Fatal(err)
	}
	if listenPort != 42000 || probeEndpoint != netip.MustParseAddrPort("10.42.0.10:42000") || reannounces != 1 || strings.Join(probedProtocols, ",") != "tcp,udp" {
		t.Fatalf("qBittorrent result port=%d probe=%s protocols=%v reannounces=%d", listenPort, probeEndpoint, probedProtocols, reannounces)
	}
}

func TestClientAcceptsNoContentOnlyAfterProtectedAuthenticationProof(t *testing.T) {
	listenPort := uint16(6881)
	preferenceReads := 0
	mutations := 0
	client := &Client{Port: 8443, ServerName: "qbittorrent.apps.test", Username: "user", Password: "password",
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v2/auth/login":
				return statusResponse(http.StatusNoContent, "", "SID_8443=session"), nil
			case "/api/v2/app/preferences":
				preferenceReads++
				if !strings.Contains(request.Header.Get("Cookie"), "SID_8443=session") {
					return statusResponse(http.StatusUnauthorized, "", ""), nil
				}
				return jsonResponse(map[string]any{"listen_port": listenPort, "random_port": false, "upnp": false}), nil
			case "/api/v2/app/setPreferences":
				mutations++
				var preferences struct {
					ListenPort uint16 `json:"listen_port"`
				}
				if err := json.Unmarshal([]byte(parseForm(t, request).Get("json")), &preferences); err != nil {
					t.Fatal(err)
				}
				listenPort = preferences.ListenPort
				return statusResponse(http.StatusNoContent, "", ""), nil
			default:
				t.Fatalf("unexpected qBittorrent request %s", request.URL.Path)
				return nil, nil
			}
		}), Probe: func(context.Context, netip.AddrPort) error { return nil }}

	if err := client.Configure(context.Background(), netip.MustParseAddr("10.42.0.10"), 42000, false); err != nil {
		t.Fatal(err)
	}
	if preferenceReads != 2 || mutations != 1 || listenPort != 42000 {
		t.Fatalf("qBittorrent reads=%d mutations=%d port=%d", preferenceReads, mutations, listenPort)
	}
}

func TestClientDoesNotMutateWhenNoContentLoginIsNotAuthorized(t *testing.T) {
	mutations := 0
	client := &Client{Port: 8443, ServerName: "qbittorrent.apps.test", Username: "user", Password: "password",
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v2/auth/login":
				return statusResponse(http.StatusNoContent, "", ""), nil
			case "/api/v2/app/preferences":
				return statusResponse(http.StatusUnauthorized, "", ""), nil
			case "/api/v2/app/setPreferences":
				mutations++
				return statusResponse(http.StatusNoContent, "", ""), nil
			default:
				t.Fatalf("unexpected qBittorrent request %s", request.URL.Path)
				return nil, nil
			}
		})}

	err := client.Configure(context.Background(), netip.MustParseAddr("10.42.0.10"), 42000, false)
	if err == nil || !strings.Contains(err.Error(), "verify qBittorrent authentication") || mutations != 0 {
		t.Fatalf("Configure() error=%v mutations=%d", err, mutations)
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
	return statusResponse(http.StatusOK, body, cookie)
}

func statusResponse(status int, body, cookie string) *http.Response {
	header := http.Header{"Content-Type": []string{"text/plain"}, "Content-Length": []string{strconv.Itoa(len(body))}}
	if cookie != "" {
		header.Set("Set-Cookie", cookie+"; Path=/; HttpOnly")
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func jsonResponse(value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
