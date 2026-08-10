// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycni "github.com/Amoenus/waycloak/internal/cni"
	"github.com/Amoenus/waycloak/internal/dataplane"
	"github.com/Amoenus/waycloak/internal/nodeagent"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var socketPath, keyFile, stateDir, nodeName, relayURL, relayToken, relayCA, releaseVersion, releaseManifestDigest, conformanceProfile string
	var cniReceiptFile, cniBinaryFile, cniConfigFile string
	var interval time.Duration
	flag.StringVar(&socketPath, "socket", waycni.DefaultAgentSocket, "root-only local CNI socket")
	flag.StringVar(&keyFile, "auth-key-file", waycni.DefaultAgentKeyFile, "per-start local protocol key")
	flag.StringVar(&stateDir, "state-dir", waycni.DefaultStateDir, "Waycloak CNI attachment state")
	flag.StringVar(&nodeName, "node-name", os.Getenv("WAYCLOAK_NODE_NAME"), "exact Kubernetes node name")
	flag.StringVar(&relayURL, "observation-relay-url", "", "HTTPS controller observation endpoint")
	flag.StringVar(&relayToken, "observation-token-file", "/var/run/secrets/waycloak-observation/token", "Pod-bound Kubernetes token")
	flag.StringVar(&relayCA, "observation-ca-file", "/var/run/secrets/waycloak-observation/ca.crt", "observation relay CA")
	flag.StringVar(&releaseVersion, "release-version", "", "immutable signed release version")
	flag.StringVar(&releaseManifestDigest, "release-manifest-digest", "", "immutable signed release manifest digest")
	flag.StringVar(&conformanceProfile, "conformance-profile", "networking.waycloak.io/Core-v1", "immutable conformance profile identity")
	flag.StringVar(&cniReceiptFile, "cni-receipt-file", "/var/lib/cni/waycloak/install-receipt.json", "root-protected exact CNI installation receipt")
	flag.StringVar(&cniBinaryFile, "cni-binary-file", "/var/run/waycloak-cni-install/waycloak-cni", "read-only installed CNI binary")
	flag.StringVar(&cniConfigFile, "cni-config-file", "/var/run/waycloak-cni-install/waycloak.conflist", "read-only active CNI conflist")
	flag.DurationVar(&interval, "reconcile-interval", 5*time.Second, "drift and observation interval")
	flag.Parse()
	if err := run(socketPath, keyFile, stateDir, nodeName, relayURL, relayToken, relayCA, releaseVersion, releaseManifestDigest, conformanceProfile, cniReceiptFile, cniBinaryFile, cniConfigFile, interval); err != nil {
		log.Fatal(err)
	}
}

func run(socketPath, keyFile, stateDir, nodeName, relayURL, relayToken, relayCA, releaseVersion, releaseManifestDigest, conformanceProfile, cniReceiptFile, cniBinaryFile, cniConfigFile string, interval time.Duration) error {
	if nodeName == "" || relayURL == "" || interval < time.Second || filepath.Dir(socketPath) != filepath.Dir(keyFile) {
		return errors.New("node name, observation relay, bounded reconcile interval, and one protected local protocol directory are required")
	}
	if !validReleaseIdentity(releaseVersion, releaseManifestDigest) || conformanceProfile == "" {
		return errors.New("exact release identity and conformance profile are required")
	}
	releaseIdentity := wayv1.ReleaseIdentity{Version: releaseVersion, ManifestDigest: releaseManifestDigest}
	if err := nodeagent.ValidateCNIInstallation(cniReceiptFile, cniBinaryFile, cniConfigFile, releaseIdentity); err != nil {
		return fmt.Errorf("CNI installation is not eligible for Core readiness: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(wayv1.AddToScheme(scheme))
	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme, LeaderElection: false, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	if err != nil {
		return fmt.Errorf("create read-only node cache: %w", err)
	}
	go func() {
		if err := manager.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("node cache stopped: %v", err)
			stop()
		}
	}()
	if !manager.GetCache().WaitForCacheSync(ctx) {
		return errors.New("node cache did not synchronize")
	}
	backend := dataplane.NewBackend()
	if err := backend.Preflight(ctx); err != nil {
		return err
	}
	bootID, err := readOpaqueID("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return fmt.Errorf("read node boot identity: %w", err)
	}
	instanceID, err := randomID()
	if err != nil {
		return err
	}
	reporter := nodeagent.Reporter{URL: relayURL, TokenFile: relayToken, CAFile: relayCA}
	service := &nodeagent.Service{
		// CNI ADD is a creation-time security boundary. Resolve the exact Pod UID
		// and node assignment from the API server, not an eventually consistent
		// controller-runtime cache that may still hold a prior name/UID pair.
		Reader: manager.GetAPIReader(), Programmer: waycni.LinuxEnforcer{Backend: backend},
		Store: waycni.FileStore{Directory: stateDir}, NodeName: nodeName, NodeBootID: bootID, InstanceID: instanceID,
		RequireRelay: true, Capabilities: []string{"nftables", "netlink", "vxlan", "ipv4", "dns-udp-tcp"},
		ReleaseIdentity:     releaseIdentity,
		ConformanceProfile:  wayv1.QualifiedName(conformanceProfile),
		WithdrawalPublisher: reporter.Report,
	}
	service.OperationErrorHook = func(operation string, err error) {
		log.Printf("local %s operation remained fail closed: %v", operation, err)
	}
	if err := recoverInstalledState(ctx, service, cniReceiptFile, cniBinaryFile, cniConfigFile, releaseIdentity); err != nil {
		log.Printf("initial fail-closed recovery incomplete: %v", err)
		service.SetBackendHealthy(false)
	} else {
		service.SetBackendHealthy(true)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	key, err := waycni.RotateProtocolKey(keyFile)
	if err != nil {
		return err
	}
	authenticator, err := waycni.NewProtocolAuthenticator(key)
	if err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return err
	}
	server := &http.Server{
		Handler:           waycni.RootPeerOnlyHandler(waycni.AuthenticatedAgentHandler(authenticator, nodeagent.Handler(service))),
		ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		IdleTimeout: 30 * time.Second, ConnContext: waycni.LocalPeerContext,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go reconcileLoop(ctx, service, reporter, cniReceiptFile, cniBinaryFile, cniConfigFile, releaseIdentity, interval)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func recoverInstalledState(ctx context.Context, service *nodeagent.Service, receiptFile, binaryFile, configFile string, releaseIdentity wayv1.ReleaseIdentity) error {
	if err := nodeagent.ValidateCNIInstallation(receiptFile, binaryFile, configFile, releaseIdentity); err != nil {
		return fmt.Errorf("CNI installation invalid: %w", err)
	}
	if err := service.LockdownAll(ctx); err != nil {
		return fmt.Errorf("restore deny-first state before serving CNI: %w", err)
	}
	// Reopening a durable allow path requires a fresh authenticated controller
	// relay handshake. The reconcile loop performs that handshake before it
	// adopts or verifies any retained attachment.
	return nil
}

func reconcileLoop(ctx context.Context, service *nodeagent.Service, reporter nodeagent.Reporter, cniReceiptFile, cniBinaryFile, cniConfigFile string, releaseIdentity wayv1.ReleaseIdentity, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		installationErr := nodeagent.ValidateCNIInstallation(cniReceiptFile, cniBinaryFile, cniConfigFile, releaseIdentity)
		if installationErr != nil {
			service.SetBackendHealthy(false)
			if lockdownErr := service.LockdownAll(ctx); lockdownErr != nil && ctx.Err() == nil {
				log.Printf("invalid-CNI lockdown incomplete: %v", lockdownErr)
			}
		}
		if publishObservation(ctx, service, reporter) && installationErr == nil {
			reconcileErr := service.ReconcileAll(ctx)
			service.SetBackendHealthy(reconcileErr == nil)
			if reconcileErr != nil && ctx.Err() == nil {
				log.Printf("fail-closed drift reconciliation incomplete: %v", reconcileErr)
			}
			// Publish the post-reconciliation state immediately. A recovering
			// node remains unadvertised until both the relay handshake and the
			// retained attachment verification succeed.
			publishObservation(ctx, service, reporter)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func publishObservation(ctx context.Context, service *nodeagent.Service, reporter nodeagent.Reporter) bool {
	reportCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := reporter.Report(reportCtx, service.Report())
	cancel()
	if err == nil {
		service.SetRelayHealthy(true)
		return true
	}
	service.SetRelayHealthy(false)
	if lockdownErr := service.LockdownAll(ctx); lockdownErr != nil && ctx.Err() == nil {
		log.Printf("controller-loss lockdown incomplete: %v", lockdownErr)
	}
	if ctx.Err() == nil {
		log.Printf("node observation publication unavailable: %v", err)
	}
	return false
}

func readOpaqueID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" || len(value) > 128 {
		return "", errors.New("opaque identity is invalid")
	}
	return value, nil
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func validReleaseIdentity(version, digest string) bool {
	if version == "" || len(version) > 128 || !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return err == nil
}
