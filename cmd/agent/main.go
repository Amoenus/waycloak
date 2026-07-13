// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Amoenus/waycloak/internal/agentconfig"
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
		return reconcileLoop(ctx, agent, load, 2*time.Second)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func reconcileLoop(ctx context.Context, agent dataplane.Agent, load func() (dataplane.Config, error), interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		cfg, err := load()
		if err == nil {
			err = agent.Repair(ctx, cfg)
		}
		if err != nil {
			log.Printf("protected-path reconciliation failed; existing deny state remains: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
