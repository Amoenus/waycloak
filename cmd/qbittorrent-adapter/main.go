// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/Amoenus/waycloak/internal/qbittorrent"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("waycloak qBitTorrent adapter: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || args[0] != "run" && args[0] != "probe" {
		return errors.New("usage: waycloak-qbittorrent-adapter <run|probe>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if args[0] == "probe" {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/readyz", contract.QBittorrentAdapterHealthPort), nil)
		if err != nil {
			return err
		}
		client := &http.Client{Timeout: time.Second}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("adapter readiness returned HTTP %d", response.StatusCode)
		}
		return nil
	}
	keyPath := strings.TrimSpace(os.Getenv("WAYCLOAK_QBITTORRENT_API_KEY_FILE"))
	if keyPath == "" {
		return errors.New("WAYCLOAK_QBITTORRENT_API_KEY_FILE is required")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read qBitTorrent API key file: %w", err)
	}
	baseURL := strings.TrimSpace(os.Getenv("WAYCLOAK_QBITTORRENT_URL"))
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	leaseEndpoint := strings.TrimSpace(os.Getenv("WAYCLOAK_LEASE_ENDPOINT"))
	if leaseEndpoint == "" {
		leaseEndpoint = fmt.Sprintf("http://127.0.0.1:%d/v1/port-forward/leases", contract.AgentLeasePort)
	}
	adapter := &qbittorrent.Adapter{Client: &qbittorrent.Client{BaseURL: baseURL, APIKey: strings.TrimSpace(string(key))}, LeaseEndpoint: leaseEndpoint, LeaseName: strings.TrimSpace(os.Getenv("WAYCLOAK_LEASE_NAME"))}
	ready := &atomic.Bool{}
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", contract.QBittorrentAdapterHealthPort), Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			http.NotFound(response, request)
			return
		}
		if !ready.Load() {
			http.Error(response, "lease generation is not applied", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
	}), ReadHeaderTimeout: time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go func() {
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("adapter readiness server failed: %v", serveErr)
			ready.Store(false)
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		reconcileCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := adapter.Reconcile(reconcileCtx)
		cancel()
		ready.Store(err == nil)
		if err != nil {
			log.Printf("lease application pending: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
