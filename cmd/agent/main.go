// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Amoenus/waycloak/internal/agentconfig"
	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/Amoenus/waycloak/internal/dataplane"
	"github.com/Amoenus/waycloak/internal/delivery"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("waycloak agent: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: waycloak-agent <prepare|verify|run|preflight|probe>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if args[0] == "probe" {
		return probeReadiness(ctx, fmt.Sprintf("http://127.0.0.1:%d/readyz", contract.AgentHealthPort))
	}
	backend := dataplane.NewBackend()
	agent := dataplane.Agent{Backend: backend}
	if args[0] == "preflight" {
		return backend.Preflight(ctx)
	}
	directory := os.Getenv("WAYCLOAK_ALLOCATION_DIR")
	if directory == "" {
		directory = agentconfig.DefaultDirectory
	}
	load := func() (dataplane.Config, error) { return agentconfig.Load(directory) }
	switch args[0] {
	case "prepare":
		cfg, err := load()
		if err != nil {
			return err
		}
		return agent.Prepare(ctx, cfg)
	case "verify":
		cfg, err := load()
		if err != nil {
			return err
		}
		return agent.Verify(ctx, cfg)
	case "run":
		return runAgent(ctx, agent, load, directory, 2*time.Second)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func probeReadiness(ctx context.Context, endpoint string) error {
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create local readiness probe: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("query local readiness: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("local readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}

func runAgent(ctx context.Context, agent dataplane.Agent, load func() (dataplane.Config, error), deliveryDirectory string, interval time.Duration) error {
	ready := &atomic.Bool{}
	deliveries := &delivery.Store{}
	_ = deliveries.Refresh(deliveryDirectory)
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", contract.AgentHealthPort))
	if err != nil {
		return fmt.Errorf("listen for readiness: %w", err)
	}
	loopbackListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", contract.AgentLeasePort))
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("listen for Pod-loopback lease delivery: %w", err)
	}
	server := &http.Server{Handler: agentHandler(ready, deliveries), ReadHeaderTimeout: 2 * time.Second}
	loopbackServer := &http.Server{Handler: leaseHandler(deliveries), ReadHeaderTimeout: 2 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = loopbackServer.Shutdown(shutdownCtx)
	}()
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("local readiness server failed: %v", serveErr)
			ready.Store(false)
		}
	}()
	go func() {
		if serveErr := loopbackServer.Serve(loopbackListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("Pod-loopback lease server failed: %v", serveErr)
		}
	}()
	go refreshDeliveries(ctx, deliveries, deliveryDirectory, interval)
	return reconcileLoop(ctx, agent, load, interval, ready)
}

func agentHandler(ready *atomic.Bool, deliveries *delivery.Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/readyz", readinessHandler(ready))
	mux.HandleFunc("/v1/port-forward/deliveries/", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		identity := request.URL.Path[len("/v1/port-forward/deliveries/"):]
		if identity == "" || strings.Contains(identity, "/") {
			http.NotFound(response, request)
			return
		}
		observation, err := deliveries.Observe(identity)
		if err != nil {
			http.NotFound(response, request)
			return
		}
		writeJSON(response, observation)
	})
	return mux
}

func leaseHandler(deliveries *delivery.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/port-forward/leases", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		document, err := deliveries.Document()
		if err != nil {
			http.Error(response, "lease delivery is unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(response, document)
	})
	mux.HandleFunc("/v1/port-forward/leases/", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		identity := request.URL.Path[len("/v1/port-forward/leases/"):]
		if identity == "" || strings.Contains(identity, "/") {
			http.NotFound(response, request)
			return
		}
		record, err := deliveries.Record(identity)
		if err != nil {
			http.NotFound(response, request)
			return
		}
		writeJSON(response, record)
	})
	return mux
}

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		log.Printf("encode local delivery response: %v", err)
	}
}

func refreshDeliveries(ctx context.Context, store *delivery.Store, directory string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = store.Refresh(directory)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func readinessHandler(ready *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "protected path is not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func reconcileLoop(ctx context.Context, agent dataplane.Agent, load func() (dataplane.Config, error), interval time.Duration, ready *atomic.Bool) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		cfg, err := load()
		if err == nil {
			err = agent.Repair(ctx, cfg)
		}
		if err != nil {
			ready.Store(false)
			log.Printf("protected-path reconciliation failed; existing deny state remains: %v", err)
		} else {
			ready.Store(true)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
