// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Amoenus/waycloak/internal/bitmagnet"
	"github.com/Amoenus/waycloak/internal/contract"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("waycloak Bitmagnet adapter: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || args[0] != "install" && args[0] != "run" && args[0] != "probe" && args[0] != "restart-probe" {
		return errors.New("usage: waycloak-bitmagnet-adapter <install|run|probe|restart-probe>")
	}
	if args[0] == "install" {
		target := strings.TrimSpace(os.Getenv("WAYCLOAK_BITMAGNET_PROBE_INSTALL_PATH"))
		if target == "" {
			target = "/waycloak-adapter-bin/bitmagnet-adapter"
		}
		return installExecutable("/proc/self/exe", target)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if args[0] == "probe" || args[0] == "restart-probe" {
		path := "/readyz"
		if args[0] == "restart-probe" {
			path = "/restartz"
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", contract.BitmagnetAdapterHealthPort, path), nil)
		if err != nil {
			return err
		}
		response, err := (&http.Client{Timeout: time.Second}).Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("adapter %s returned HTTP %d", path, response.StatusCode)
		}
		return nil
	}
	leaseEndpoint := strings.TrimSpace(os.Getenv(contract.AdapterLeaseEndpointEnv))
	if leaseEndpoint == "" {
		leaseEndpoint = fmt.Sprintf("http://127.0.0.1:%d/v1/port-forward/leases", contract.AgentLeasePort)
	}
	configPath := strings.TrimSpace(os.Getenv("WAYCLOAK_BITMAGNET_CONFIG_FILE"))
	if configPath == "" {
		configPath = "/root/.config/bitmagnet/config.yml"
	}
	protocol := strings.TrimSpace(os.Getenv(contract.AdapterProtocolEnv))
	if protocol != "" && protocol != contract.AdapterProtocolVersion {
		return fmt.Errorf("unsupported adapter protocol %q", protocol)
	}
	adapter := &bitmagnet.Adapter{LeaseEndpoint: leaseEndpoint, LeaseName: strings.TrimSpace(os.Getenv("WAYCLOAK_LEASE_NAME")), ConfigPath: configPath}
	ready := &atomic.Bool{}
	restartRequired := &atomic.Bool{}
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", contract.BitmagnetAdapterHealthPort), Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/readyz":
			if !ready.Load() {
				http.Error(response, "lease generation is not applied", http.StatusServiceUnavailable)
				return
			}
		case "/restartz":
			if restartRequired.Load() {
				http.Error(response, "Bitmagnet restart is required for the staged port", http.StatusServiceUnavailable)
				return
			}
		default:
			http.NotFound(response, request)
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
	serveErrors := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastMessage := ""
	lastLog := time.Time{}
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		revision, err := adapter.Reconcile(attempt)
		cancel()
		if revision.ConfigChanged {
			restartRequired.Store(true)
		}
		ready.Store(err == nil)
		if err == nil {
			restartRequired.Store(false)
			if lastMessage != "ready" {
				log.Printf("lease generation %d is applied", revision.Generation)
			}
			lastMessage = "ready"
		} else if err.Error() != lastMessage || time.Since(lastLog) >= time.Minute {
			log.Printf("lease application pending: %v", err)
			lastMessage = err.Error()
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-serveErrors:
			return err
		case <-ticker.C:
		}
	}
}

func installExecutable(sourcePath, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create adapter binary directory: %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open adapter executable: %w", err)
	}
	defer source.Close()
	temporary, err := os.CreateTemp(filepath.Dir(target), ".bitmagnet-adapter-*")
	if err != nil {
		return fmt.Errorf("create temporary adapter executable: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := io.Copy(temporary, source); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("copy adapter executable: %w", err)
	}
	if err := temporary.Chmod(0o555); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set adapter executable mode: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close adapter executable: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("install adapter executable: %w", err)
	}
	return nil
}
