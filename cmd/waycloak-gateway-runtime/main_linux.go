// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

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
	"github.com/Amoenus/waycloak/internal/provider/gluetun"
)

func main() {
	var listenAddress, serverCert, serverKey, clientCA, controllerIdentity, gatewayUID, tunnelInterface, overlayInterface, portForwardCapability, controlAPIKeyFile string
	var writeControlAuth, controlIdentitySource, controlAPIKeyOutput string
	var adapterCA, adapterCert, adapterKey, clusterDomain string
	var adapterPort uint
	flag.StringVar(&listenAddress, "listen-address", ":9443", "mTLS gateway-runtime listener")
	flag.StringVar(&serverCert, "tls-cert", "", "gateway-runtime serving certificate")
	flag.StringVar(&serverKey, "tls-key", "", "gateway-runtime serving private key")
	flag.StringVar(&clientCA, "client-ca", "", "CA bundle for the exact controller client identity")
	flag.StringVar(&controllerIdentity, "controller-identity", "spiffe://waycloak.io/replacement-controller", "exact authorized controller SPIFFE URI")
	flag.StringVar(&gatewayUID, "gateway-uid", "", "exact VPNGateway UID served by this runtime")
	flag.StringVar(&tunnelInterface, "tunnel-interface", "tun0", "exact VPN tunnel interface")
	flag.StringVar(&overlayInterface, "overlay-interface", "waycloak0", "exact protected overlay interface")
	flag.StringVar(&portForwardCapability, "engine-port-forward-capability", "", "exact port-forward capability selected by the VPN engine adapter")
	flag.StringVar(&controlAPIKeyFile, "gluetun-control-api-key-file", "", "Pod-local Gluetun control API identity")
	flag.StringVar(&writeControlAuth, "write-gluetun-control-auth", "", "write exact Gluetun control policy and exit")
	flag.StringVar(&controlIdentitySource, "gluetun-control-identity-source", "", "private identity source used only while generating control policy")
	flag.StringVar(&controlAPIKeyOutput, "gluetun-control-api-key-output", "", "API key output used only while generating control policy")
	flag.StringVar(&adapterCA, "adapter-ca", "", "CA bundle for out-of-process adapter services")
	flag.StringVar(&adapterCert, "adapter-client-cert", "", "adapter-protocol client certificate")
	flag.StringVar(&adapterKey, "adapter-client-key", "", "adapter-protocol client private key")
	flag.UintVar(&adapterPort, "adapter-port", uint(portforward.DefaultAdapterPort), "deterministic adapter Service HTTPS port")
	flag.StringVar(&clusterDomain, "cluster-domain", "", "reviewed Kubernetes cluster DNS suffix")
	flag.Parse()
	if writeControlAuth != "" || controlIdentitySource != "" || controlAPIKeyOutput != "" {
		if err := gluetun.WriteControlAuthentication(controlIdentitySource, writeControlAuth, controlAPIKeyOutput); err != nil {
			slog.Error("generate Gluetun control authentication", "error", err)
			os.Exit(1)
		}
		return
	}

	if listenAddress == "" || serverCert == "" || serverKey == "" || clientCA == "" || controllerIdentity == "" || gatewayUID == "" || tunnelInterface == "" || overlayInterface == "" || portForwardCapability == "" || controlAPIKeyFile == "" {
		slog.Error("exact gateway identity, interfaces, listener, and mTLS files are required")
		os.Exit(1)
	}
	if (adapterCA == "") != (adapterCert == "") || (adapterCA == "") != (adapterKey == "") || adapterPort == 0 || adapterPort > 65535 || adapterCA != "" && clusterDomain == "" {
		slog.Error("adapter mTLS identity, cluster domain, and port must be complete")
		os.Exit(1)
	}
	serverTLS, err := portforward.ServerTLSConfig(clientCA, controllerIdentity)
	if err != nil {
		slog.Error("load gateway-runtime client trust", "error", err)
		os.Exit(1)
	}
	var adapter portforward.AdapterProtocol
	if adapterCA != "" {
		adapter, err = portforward.NewHTTPAdapterClient(adapterCA, adapterCert, adapterKey, uint16(adapterPort), clusterDomain)
		if err != nil {
			slog.Error("configure adapter protocol", "error", err)
			os.Exit(1)
		}
	}
	capability, err := gluetun.NewPortForwardCapability(gluetun.PortForwardOptions{CapabilityName: portForwardCapability, APIKeyFile: controlAPIKeyFile})
	if err != nil {
		slog.Error("configure engine port-forward capability", "error", err)
		os.Exit(1)
	}
	manager := &portforward.GatewayRuntimeManager{
		PortForward: capability,
		Rules:       portforward.LinuxRuleBackend{TunnelInterface: tunnelInterface, OverlayInterface: overlayInterface},
		Delivery:    portforward.DeliveryManager{Adapter: adapter},
	}
	server := &http.Server{
		Addr: listenAddress, Handler: portforward.RuntimeHandler{Manager: manager, GatewayUID: wayv1.ObjectUID(gatewayUID)}, TLSConfig: serverTLS,
		ErrorLog: httpserverlog.NewTLSProbeErrorLogger(os.Stderr), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServeTLS(serverCert, serverKey); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve gateway runtime", "error", err)
		os.Exit(1)
	}
}
