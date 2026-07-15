// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQBittorrentProxyUsesUpstreamHost(t *testing.T) {
	hosts := make(chan string, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		hosts <- request.Host
		response.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()
	upstream, err := url.Parse(upstreamServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(newQBittorrentProxy(upstream, "", 0))
	defer proxyServer.Close()

	response, err := proxyServer.Client().Get(proxyServer.URL + "/api/v2/app/preferences")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("proxy status = %d", response.StatusCode)
	}
	if got := <-hosts; got != upstream.Host {
		t.Fatalf("upstream Host = %q, want %q", got, upstream.Host)
	}
}

func TestQBittorrentProxyInjectsAndRecoversFromPreferenceStall(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()
	upstream, err := url.Parse(upstreamServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	stallFile := filepath.Join(t.TempDir(), "stall")
	if err := os.WriteFile(stallFile, []byte("stall"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(newQBittorrentProxy(upstream, stallFile, 100*time.Millisecond))
	defer proxyServer.Close()
	client := &http.Client{Timeout: 10 * time.Millisecond}
	if _, err := client.Get(proxyServer.URL + "/api/v2/app/preferences"); err == nil {
		t.Fatal("preference request did not time out while the stall marker existed")
	}
	if err := os.Remove(stallFile); err != nil {
		t.Fatal(err)
	}
	client.Timeout = time.Second
	response, err := client.Get(proxyServer.URL + "/api/v2/app/preferences")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("recovered proxy status = %d", response.StatusCode)
	}
}
