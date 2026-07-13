// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	waygateway "github.com/Amoenus/waycloak/internal/gateway"
	"github.com/Amoenus/waycloak/internal/provider"
	"github.com/Amoenus/waycloak/internal/provider/gluetun"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("waycloak gateway manager: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: gateway-manager run [flags]")
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	engineType := flags.String("engine-type", "", "VPN engine adapter")
	healthAddress := flags.String("health-address", fmt.Sprintf(":%d", waygateway.HealthPort), "readiness listen address")
	engineHealthURL := flags.String("engine-health-url", "http://127.0.0.1:9999/", "engine health endpoint")
	engineControlURL := flags.String("engine-control-url", "http://127.0.0.1:8000", "engine control endpoint")
	configPath := flags.String("config-path", "", "gateway desired-state JSON path")
	_ = flags.String("overlay-cidr", "", "reserved for the overlay reconciler")
	_ = flags.Int("vni", 0, "reserved for the overlay reconciler")
	_ = flags.Int("mtu", 0, "reserved for the overlay reconciler")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	engine, err := engineFor(*engineType, *engineHealthURL, *engineControlURL)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	manager := &waygateway.HealthManager{Engine: engine}
	if *configPath != "" {
		manager.Source = waygateway.FileSource{Path: *configPath}
	}
	return serve(ctx, manager, *healthAddress, 2*time.Second)
}

func engineFor(engineType, healthURL, controlURL string) (provider.VPNEngine, error) {
	if !strings.EqualFold(engineType, "Gluetun") {
		return nil, fmt.Errorf("unsupported engine type %q", engineType)
	}
	engine := gluetun.New()
	engine.HealthURL = healthURL
	engine.ControlURL = strings.TrimRight(controlURL, "/")
	return engine, nil
}

func serve(ctx context.Context, manager *waygateway.HealthManager, address string, interval time.Duration) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) {
		if !manager.Ready() {
			http.Error(response, "engine path is not ready", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		manager.Reconcile(ctx)
		if err := manager.Error(); err != nil {
			log.Printf("engine observation failed: %v", err)
		}
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return server.Shutdown(shutdownCtx)
		case err := <-serverErrors:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
		}
	}
}
