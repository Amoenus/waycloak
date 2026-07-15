// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	leaseAddress := flag.String("lease-address", "127.0.0.1:9809", "lease API listen address")
	trackerAddress := flag.String("tracker-address", "127.0.0.1:18081", "tracker listen address")
	documentPath := flag.String("document", "", "delivery document path")
	stateDirectory := flag.String("state-directory", "/tmp", "observation output directory")
	qbitProxyAddress := flag.String("qbittorrent-proxy-address", "", "optional qBitTorrent API proxy listen address")
	qbitUpstream := flag.String("qbittorrent-upstream", "http://127.0.0.1:8080", "qBitTorrent API proxy upstream")
	qbitStallFile := flag.String("qbittorrent-stall-file", "", "file whose presence stalls qBitTorrent preference observations")
	flag.Parse()
	if *documentPath == "" {
		log.Fatal("delivery document path is required")
	}
	leaseMux := http.NewServeMux()
	leaseMux.HandleFunc("/v1/port-forward/leases", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/port-forward/leases" {
			http.NotFound(response, request)
			return
		}
		data, err := os.ReadFile(*documentPath)
		if err != nil {
			http.Error(response, "document unavailable", http.StatusServiceUnavailable)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(data)
	})
	leaseMux.HandleFunc("/v1/port-forward/leases/", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/ack") {
			http.NotFound(response, request)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 4096))
		if err != nil {
			http.Error(response, "invalid acknowledgement", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(filepath.Join(*stateDirectory, "ack.json"), data, 0o600); err != nil {
			http.Error(response, "store acknowledgement", http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(filepath.Join(*stateDirectory, "ack-path"), []byte(request.URL.Path+"\n"), 0o600); err != nil {
			http.Error(response, "store acknowledgement path", http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	trackerMux := http.NewServeMux()
	trackerMux.HandleFunc("/announce", func(response http.ResponseWriter, request *http.Request) {
		port := request.URL.Query().Get("port")
		if _, err := url.ParseQuery(request.URL.RawQuery); err != nil || port == "" {
			http.Error(response, "invalid announce", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(filepath.Join(*stateDirectory, "tracker-port"), []byte(port+"\n"), 0o600); err != nil {
			http.Error(response, "store tracker observation", http.StatusInternalServerError)
			return
		}
		_, _ = response.Write([]byte("d8:intervali60e5:peers0:e"))
	})
	errors := make(chan error, 3)
	go func() {
		server := &http.Server{Addr: *leaseAddress, Handler: leaseMux, ReadHeaderTimeout: 2 * time.Second}
		errors <- fmt.Errorf("lease server: %w", server.ListenAndServe())
	}()
	go func() {
		server := &http.Server{Addr: *trackerAddress, Handler: trackerMux, ReadHeaderTimeout: 2 * time.Second}
		errors <- fmt.Errorf("tracker server: %w", server.ListenAndServe())
	}()
	if *qbitProxyAddress != "" {
		upstream, err := url.Parse(*qbitUpstream)
		if err != nil || upstream.Scheme != "http" || upstream.Host == "" {
			log.Fatal("qBitTorrent proxy upstream must be an HTTP URL")
		}
		handler := newQBittorrentProxy(upstream, *qbitStallFile, 3*time.Second)
		go func() {
			server := &http.Server{Addr: *qbitProxyAddress, Handler: handler, ReadHeaderTimeout: 2 * time.Second}
			errors <- fmt.Errorf("qBitTorrent proxy server: %w", server.ListenAndServe())
		}()
	}
	log.Fatal(<-errors)
}

func newQBittorrentProxy(upstream *url.URL, stallFile string, stallDuration time.Duration) http.Handler {
	proxy := &httputil.ReverseProxy{Rewrite: func(proxyRequest *httputil.ProxyRequest) {
		proxyRequest.SetURL(upstream)
		// qBitTorrent rejects a Host port that differs from its local WebUI
		// listener, so the test proxy must identify the actual upstream.
		proxyRequest.Out.Host = upstream.Host
	}}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v2/app/preferences" && stallFile != "" {
			if _, err := os.Stat(stallFile); err == nil {
				time.Sleep(stallDuration)
			}
		}
		proxy.ServeHTTP(response, request)
	})
}
