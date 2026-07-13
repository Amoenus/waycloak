// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// fake-gluetun implements only the documented observation endpoints used by
// gateway-manager tests. It is not a VPN engine and forwards no traffic.
package main

import (
	"context"
	"errors"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	health := &http.Server{Addr: "127.0.0.1:9999", Handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) }), ReadHeaderTimeout: 2 * time.Second}
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/v1/vpn/status", jsonResponse(`{"status":"running"}`))
	controlMux.HandleFunc("/v1/dns/status", jsonResponse(`{"status":"running"}`))
	controlMux.HandleFunc("/v1/publicip/ip", jsonResponse(`{"public_ip":"203.0.113.10"}`))
	control := &http.Server{Addr: "127.0.0.1:8000", Handler: controlMux, ReadHeaderTimeout: 2 * time.Second}
	errorsChannel := make(chan error, 2)
	go func() { errorsChannel <- health.ListenAndServe() }()
	go func() { errorsChannel <- control.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err := <-errorsChannel:
		if !errors.Is(err, http.ErrServerClosed) {
			panic(err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = health.Shutdown(shutdownCtx)
	_ = control.Shutdown(shutdownCtx)
}

func jsonResponse(body string) http.HandlerFunc {
	return func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(body))
	}
}
