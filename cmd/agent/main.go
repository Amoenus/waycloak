// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Amoenus/waycloak/internal/agentconfig"
	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/Amoenus/waycloak/internal/dataplane"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("waycloak agent: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: waycloak-agent <prepare|verify|run|preflight>")
	}
	backend := dataplane.NewBackend()
	agent := dataplane.Agent{Backend: backend}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if args[0] == "preflight" {
		return backend.Preflight(ctx)
	}
	directory := os.Getenv("WAYCLOAK_ALLOCATION_DIR")
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
		return runAgent(ctx, agent, load, 2*time.Second)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runAgent(ctx context.Context, agent dataplane.Agent, load func() (dataplane.Config, error), interval time.Duration) error {
	ready := &atomic.Bool{}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", contract.AgentHealthPort))
	if err != nil {
		return fmt.Errorf("listen for readiness: %w", err)
	}
	server := &http.Server{Handler: readinessHandler(ready), ReadHeaderTimeout: 2 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("local readiness server failed: %v", serveErr)
			ready.Store(false)
		}
	}()
	return reconcileLoop(ctx, agent, load, interval, ready)
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
