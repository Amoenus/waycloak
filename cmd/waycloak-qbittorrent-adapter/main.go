// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"github.com/Amoenus/waycloak/internal/httpserverlog"
	"github.com/Amoenus/waycloak/internal/portforward"
	"github.com/Amoenus/waycloak/internal/qbittorrentadapter"
	"github.com/Amoenus/waycloak/internal/telemetry"
)

func main() {
	var listenAddress, serverCert, serverKey, clientCA, gatewayIdentity, controllerIdentity string
	var namespace, name, image, podUID, stateFile string
	var qbitCA, qbitServerName, qbitUsernameFile, qbitPasswordFile string
	var qbitPort uint
	var telemetryOptions telemetry.Options
	flag.StringVar(&listenAddress, "listen-address", ":9444", "mTLS adapter listener")
	flag.StringVar(&serverCert, "tls-cert", "", "adapter serving certificate")
	flag.StringVar(&serverKey, "tls-key", "", "adapter serving private key")
	flag.StringVar(&clientCA, "client-ca", "", "CA bundle for the exact gateway-runtime client identity")
	flag.StringVar(&gatewayIdentity, "gateway-identity", "spiffe://waycloak.io/gateway-runtime", "exact authorized gateway-runtime SPIFFE URI")
	flag.StringVar(&controllerIdentity, "controller-identity", "spiffe://waycloak.io/replacement-controller", "exact authorized controller SPIFFE URI for health only")
	flag.StringVar(&namespace, "namespace", "", "WorkloadAdapter namespace")
	flag.StringVar(&name, "name", "", "WorkloadAdapter name")
	flag.StringVar(&image, "image", "", "immutable WorkloadAdapter image identity")
	flag.StringVar(&podUID, "pod-uid", "", "exact adapter Pod UID from the downward API")
	flag.StringVar(&stateFile, "state-file", "", "durable adapter state file")
	flag.StringVar(&qbitCA, "qbittorrent-ca", "", "CA bundle for qBittorrent WebUI TLS")
	flag.StringVar(&qbitServerName, "qbittorrent-server-name", "", "exact qBittorrent WebUI TLS name")
	flag.StringVar(&qbitUsernameFile, "qbittorrent-username-file", "", "workload-owned qBittorrent username file")
	flag.StringVar(&qbitPasswordFile, "qbittorrent-password-file", "", "workload-owned qBittorrent password file")
	flag.UintVar(&qbitPort, "qbittorrent-port", 8080, "qBittorrent WebUI HTTPS port on the exact selected Pod address")
	telemetryOptions.BindFlags(flag.CommandLine, "waycloak-qbittorrent-adapter")
	flag.Parse()

	if listenAddress == "" || serverCert == "" || serverKey == "" || clientCA == "" || gatewayIdentity == "" || controllerIdentity == "" || namespace == "" || name == "" || image == "" || podUID == "" || stateFile == "" || qbitPort == 0 || qbitPort > 65535 {
		slog.Error("exact adapter identity, durable state, listener, and mTLS files are required")
		os.Exit(1)
	}
	application, err := qbittorrentadapter.NewClient(qbitCA, qbitServerName, qbitUsernameFile, qbitPasswordFile, uint16(qbitPort))
	if err != nil {
		slog.Error("configure qBittorrent client", "error", err)
		os.Exit(1)
	}
	handler := &qbittorrentadapter.Handler{Application: application, Namespace: wayv1.NamespaceName(namespace), Name: wayv1.ObjectName(name),
		Image: image, PodUID: wayv1.ObjectUID(podUID), StateFile: stateFile, HealthIdentity: controllerIdentity, RuntimeIdentity: gatewayIdentity}
	if err := handler.Load(); err != nil {
		slog.Error("load durable adapter state", "error", err)
		os.Exit(1)
	}
	serverTLS, err := portforward.ServerTLSConfigForIdentities(clientCA, gatewayIdentity, controllerIdentity)
	if err != nil {
		slog.Error("load adapter client trust", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	telemetryRuntime, err := telemetryOptions.Start(ctx)
	if err != nil {
		slog.Error("configure telemetry", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = telemetryRuntime.Shutdown(shutdown)
	}()
	handler.Telemetry = telemetryRuntime.Recorder
	server := &http.Server{Addr: listenAddress, Handler: handler, TLSConfig: serverTLS, ErrorLog: httpserverlog.NewTLSProbeErrorLogger(os.Stderr), ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServeTLS(serverCert, serverKey); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve qBittorrent adapter", "error", err)
		os.Exit(1)
	}
}
