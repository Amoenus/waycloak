// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	Config  Config
	Backend Backend
	Engine  Engine
	healthy atomic.Bool
}

func (service *Service) Reconcile(ctx context.Context) error {
	if service.Backend == nil || service.Engine == nil {
		return errors.New("gateway backend and engine observation are required")
	}
	if err := service.Backend.EnsureOverlay(ctx, service.Config); err != nil {
		service.healthy.Store(false)
		return err
	}
	observation, err := service.Engine.Observe(ctx)
	if err != nil || !observation.TunnelReady || !observation.DNSReady {
		service.healthy.Store(false)
		rulesErr := service.Backend.ReplaceRules(ctx, service.Config, false)
		if err == nil {
			err = errors.New("gateway engine tunnel or DNS is not ready")
		}
		return errors.Join(err, rulesErr)
	}
	if err := service.Backend.ReplaceRules(ctx, service.Config, true); err != nil {
		service.healthy.Store(false)
		return err
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
	healthServer := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if !service.healthy.Load() {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = healthServer.Shutdown(shutdown)
	}()
	go func() { _ = healthServer.Serve(healthListener) }()
	if err := runDNS(ctx, service.Config); err != nil {
		_ = healthServer.Close()
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		reconcileCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := service.Reconcile(reconcileCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			service.healthy.Store(false)
		}
		select {
		case <-ctx.Done():
			_ = service.Backend.ReplaceRules(context.Background(), service.Config, false)
			return nil
		case <-ticker.C:
		}
	}
}

func runDNS(ctx context.Context, config Config) error {
	address := net.JoinHostPort(config.GatewayAddress.String(), "53")
	upstream := config.DNSUpstream.String()
	udp, err := net.ListenPacket("udp4", address)
	if err != nil {
		return fmt.Errorf("listen gateway DNS UDP: %w", err)
	}
	tcp, err := net.Listen("tcp4", address)
	if err != nil {
		udp.Close()
		return fmt.Errorf("listen gateway DNS TCP: %w", err)
	}
	go func() { <-ctx.Done(); udp.Close(); tcp.Close() }()
	go serveUDP(ctx, udp, upstream)
	go serveTCP(ctx, tcp, upstream)
	return nil
}

func serveUDP(ctx context.Context, listener net.PacketConn, upstream string) {
	const maximumConcurrentRequests = 128
	semaphore := make(chan struct{}, maximumConcurrentRequests)
	buffer := make([]byte, 65535)
	for {
		count, peer, err := listener.ReadFrom(buffer)
		if err != nil {
			return
		}
		request := append([]byte(nil), buffer[:count]...)
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}
		go func() {
			defer func() { <-semaphore }()
			dialer := net.Dialer{Timeout: 2 * time.Second}
			connection, err := dialer.DialContext(ctx, "udp4", upstream)
			if err != nil {
				return
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err = connection.Write(request); err != nil {
				return
			}
			response := make([]byte, 65535)
			count, err := connection.Read(response)
			if err == nil {
				_, _ = listener.WriteTo(response[:count], peer)
			}
		}()
	}
}
func serveTCP(ctx context.Context, listener net.Listener, upstream string) {
	const maximumConcurrentConnections = 128
	semaphore := make(chan struct{}, maximumConcurrentConnections)
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			_ = client.Close()
			return
		}
		go func() {
			defer func() { <-semaphore }()
			defer client.Close()
			upstreamConnection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp4", upstream)
			if err != nil {
				return
			}
			defer upstreamConnection.Close()
			go func() {
				_, _ = io.Copy(upstreamConnection, client)
				if connection, ok := upstreamConnection.(*net.TCPConn); ok {
					_ = connection.CloseWrite()
				}
			}()
			_, _ = io.Copy(client, upstreamConnection)
		}()
	}
}
