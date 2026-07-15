// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
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
	"github.com/Amoenus/waycloak/internal/provider/proton"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("waycloak gateway manager: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gateway-manager <run|render-engine-firewall> [flags]")
	}
	if args[0] == "render-engine-firewall" {
		return renderEngineFirewall(args[1:])
	}
	if args[0] != "run" {
		return errors.New("usage: gateway-manager <run|render-engine-firewall> [flags]")
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	engineType := flags.String("engine-type", "", "VPN engine adapter")
	healthAddress := flags.String("health-address", fmt.Sprintf(":%d", waygateway.HealthPort), "readiness listen address")
	engineHealthURL := flags.String("engine-health-url", "http://127.0.0.1:9999/", "engine health endpoint")
	engineControlURL := flags.String("engine-control-url", "http://127.0.0.1:8000", "engine control endpoint")
	portForwardDriver := flags.String("port-forward-driver", "", "provider port-forward driver")
	tunnelInterface := flags.String("tunnel-interface", waygateway.TunnelInterface, "VPN tunnel interface")
	natPMPGatewayAddress := flags.String("nat-pmp-gateway-address", "", "optional Proton NAT-PMP gateway address override")
	configPath := flags.String("config-path", "", "gateway desired-state JSON path")
	resolvConf := flags.String("resolv-conf", "/etc/resolv.conf", "captured Kubernetes resolver configuration")
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
	driver, err := portForwardDriverFor(*portForwardDriver, *tunnelInterface, *natPMPGatewayAddress)
	if err != nil {
		return err
	}
	if driver != nil {
		manager.PortForwarding = &waygateway.PortForwardManager{Driver: driver}
	}
	if *configPath != "" {
		dns, err := waygateway.NewDNSProxyFromResolvConf(*resolvConf)
		if err != nil {
			return err
		}
		manager.Source = waygateway.FileSource{Path: *configPath}
		manager.Network = waygateway.NewNetwork()
		manager.Forwarding = waygateway.NewForwarding()
		manager.DNS = dns
	}
	return serve(ctx, manager, *healthAddress, 2*time.Second)
}

func portForwardDriverFor(driverName, tunnelInterface, gatewayAddress string) (provider.PortForwardDriver, error) {
	if driverName == "" {
		return nil, nil
	}
	if !strings.EqualFold(driverName, "ProtonNatPmp") {
		return nil, fmt.Errorf("unsupported port-forward driver %q", driverName)
	}
	driver := proton.New(tunnelInterface)
	driver.GatewayAddress = gatewayAddress
	return driver, nil
}

func renderEngineFirewall(args []string) error {
	flags := flag.NewFlagSet("render-engine-firewall", flag.ContinueOnError)
	basePath := flags.String("base-path", "", "controller-rendered post-rules template")
	resolvConf := flags.String("resolv-conf", "/etc/resolv.conf", "Pod resolver configuration")
	outputPath := flags.String("output", "", "rendered Gluetun post-rules path")
	resolverOutputPath := flags.String("resolver-output", "", "captured Kubernetes resolver configuration path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *basePath == "" || *outputPath == "" || *resolverOutputPath == "" {
		return errors.New("--base-path, --output, and --resolver-output are required")
	}
	base, err := os.ReadFile(*basePath)
	if err != nil {
		return fmt.Errorf("read engine firewall template: %w", err)
	}
	resolver, err := waygateway.ResolverConfigFromFile(*resolvConf)
	if err != nil {
		return err
	}
	rendered, err := waygateway.RenderEnginePostRules(string(base), resolver)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*outputPath, []byte(rendered), 0o600); err != nil {
		return fmt.Errorf("write rendered engine firewall: %w", err)
	}
	capturedResolver, err := resolver.Render()
	if err != nil {
		return err
	}
	if err := os.WriteFile(*resolverOutputPath, []byte(capturedResolver), 0o600); err != nil {
		return fmt.Errorf("write captured Kubernetes resolver configuration: %w", err)
	}
	return nil
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
	mux := managerHandler(manager)
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
		if err := manager.PortForwardingError(); err != nil {
			log.Printf("port-forward reconciliation failed: %v", err)
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

func managerHandler(manager *waygateway.HealthManager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !manager.Ready() {
			http.Error(response, "engine path is not ready", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/gateway/observation", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		document := waygateway.ManagerObservationDocument{APIVersion: waygateway.ManagerObservationAPIVersion, Observation: waygateway.ManagerObservation{AppliedMembershipGeneration: manager.AppliedMembershipGeneration()}}
		if err := json.NewEncoder(response).Encode(document); err != nil {
			log.Printf("encode gateway observation: %v", err)
		}
	})
	mux.HandleFunc("/v1/port-forward/leases/", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		identity := strings.TrimPrefix(request.URL.Path, "/v1/port-forward/leases/")
		if identity == "" || strings.Contains(identity, "/") || manager.PortForwarding == nil {
			http.NotFound(response, request)
			return
		}
		var selected *waygateway.PortForwardObservation
		for _, observation := range manager.PortForwardingSnapshot() {
			if observation.Identity == identity {
				copy := observation
				selected = &copy
				break
			}
		}
		if selected == nil {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		document := waygateway.PortForwardObservationDocument{APIVersion: waygateway.PortForwardObservationAPIVersion, Lease: *selected}
		if err := json.NewEncoder(response).Encode(document); err != nil {
			log.Printf("encode port-forward observations: %v", err)
		}
	})
	return mux
}
