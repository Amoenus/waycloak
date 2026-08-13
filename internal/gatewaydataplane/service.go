// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

type Backend interface {
	EnsureOverlay(context.Context, Config) error
	ReplaceRules(context.Context, Config, bool) error
}
type Engine interface {
	Observe(context.Context) (provider.EngineObservation, error)
}

type Service struct {
	Config                Config
	Backend               Backend
	Engine                Engine
	DNSProber             DNSProber
	ReconcileErrorHook    func(error)
	ReconcileRecoveryHook func(previousError string, unavailableFor time.Duration)
	healthy               atomic.Bool
	tunnelReady           atomic.Bool
	dnsReady              atomic.Bool
}

type HealthStatus struct {
	Ready       bool `json:"ready"`
	TunnelReady bool `json:"tunnelReady"`
	DNSReady    bool `json:"dnsReady"`
}

func (service *Service) Reconcile(ctx context.Context) error {
	if service.Backend == nil || service.Engine == nil || service.DNSProber == nil {
		return errors.New("gateway backend, engine observation, and split-DNS probe are required")
	}
	if err := service.Backend.EnsureOverlay(ctx, service.Config); err != nil {
		service.healthy.Store(false)
		service.tunnelReady.Store(false)
		service.dnsReady.Store(false)
		return errors.Join(err, service.Backend.ReplaceRules(ctx, service.Config, false))
	}
	observation, err := service.Engine.Observe(ctx)
	service.tunnelReady.Store(err == nil && observation.TunnelReady)
	service.dnsReady.Store(false)
	if err != nil || !observation.TunnelReady || !observation.DNSReady {
		service.healthy.Store(false)
		rulesErr := service.Backend.ReplaceRules(ctx, service.Config, false)
		if err == nil {
			err = errors.New("gateway engine tunnel or DNS is not ready")
		}
		return errors.Join(err, rulesErr)
	}
	// A recovering gateway needs its exact cluster-DNS route and OUTPUT
	// allowance before the end-to-end probe can succeed. Reassert only while
	// unready so stable reconciliation never withdraws a healthy forward path.
	if !service.healthy.Load() {
		if err := service.Backend.ReplaceRules(ctx, service.Config, false); err != nil {
			return err
		}
	}
	if err := service.DNSProber.Probe(ctx); err != nil {
		service.healthy.Store(false)
		rulesErr := service.Backend.ReplaceRules(ctx, service.Config, false)
		return errors.Join(fmt.Errorf("gateway split-DNS observation failed: %w", err), rulesErr)
	}
	service.dnsReady.Store(true)
	if err := service.Backend.ReplaceRules(ctx, service.Config, true); err != nil {
		service.healthy.Store(false)
		return errors.Join(err, service.Backend.ReplaceRules(ctx, service.Config, false))
	}
	service.healthy.Store(true)
	return nil
}

func (service *Service) Run(ctx context.Context, interval time.Duration) error {
	if interval < time.Second {
		return errors.New("gateway reconcile interval must be at least one second")
	}
	if err := service.Config.Validate(); err != nil {
		return err
	}
	if err := service.Backend.ReplaceRules(ctx, service.Config, false); err != nil {
		return err
	}
	if err := service.Backend.EnsureOverlay(ctx, service.Config); err != nil {
		return err
	}
	healthListener, err := net.Listen("tcp4", fmt.Sprintf(":%d", service.Config.HealthPort))
	if err != nil {
		return fmt.Errorf("listen gateway health endpoint: %w", err)
	}
	healthServer := &http.Server{Handler: service.healthHandler(), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = healthServer.Shutdown(shutdown)
	}()
	go func() { _ = healthServer.Serve(healthListener) }()
	dnsProxy, err := startDNSProxy(ctx, service.Config)
	if err != nil {
		_ = healthServer.Close()
		return err
	}
	service.DNSProber = dnsProxy
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastReconcileError := ""
	var unavailableSince time.Time
	for {
		reconcileCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := service.Reconcile(reconcileCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			service.healthy.Store(false)
		}
		previousReconcileError := lastReconcileError
		if err != nil && unavailableSince.IsZero() {
			unavailableSince = time.Now()
		}
		lastReconcileError = service.reportReconcileError(err, lastReconcileError)
		if err == nil && previousReconcileError != "" {
			service.reportReconcileRecovery(previousReconcileError, unavailableSince)
			unavailableSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			_ = service.Backend.ReplaceRules(context.Background(), service.Config, false)
			return nil
		case <-ticker.C:
		}
	}
}

func (service *Service) healthHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/status" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(service.HealthStatus())
			return
		}
		if request.URL.Path != "/readyz" {
			http.NotFound(writer, request)
			return
		}
		if !service.healthy.Load() {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
}

func (service *Service) HealthStatus() HealthStatus {
	return HealthStatus{Ready: service.healthy.Load(), TunnelReady: service.tunnelReady.Load(), DNSReady: service.dnsReady.Load()}
}

func (service *Service) reportReconcileError(err error, previous string) string {
	if err == nil {
		return ""
	}
	current := err.Error()
	if current != previous && service.ReconcileErrorHook != nil {
		service.ReconcileErrorHook(err)
	}
	return current
}

func (service *Service) reportReconcileRecovery(previous string, unavailableSince time.Time) {
	if previous != "" && !unavailableSince.IsZero() && service.ReconcileRecoveryHook != nil {
		service.ReconcileRecoveryHook(previous, time.Since(unavailableSince))
	}
}

func dnsListenAddress(config Config) string {
	return net.JoinHostPort(config.GatewayAddress.String(), strconv.Itoa(int(DNSListenPort)))
}
