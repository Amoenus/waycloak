// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	address := flag.String("address", ":8443", "HTTPS listen address")
	certificate := flag.String("tls-cert", "/tls/tls.crt", "TLS certificate")
	key := flag.String("tls-key", "/tls/tls.key", "TLS private key")
	flag.Parse()
	server := &http.Server{Addr: *address, Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil || net.ParseIP(host) == nil {
			http.Error(response, "invalid peer", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintln(response, host)
	}), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServeTLS(*certificate, *key); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
