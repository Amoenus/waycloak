// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func runObservationCertificateBootstrap(arguments []string) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("load in-cluster configuration: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}
	return ensureObservationCertificateBootstrap(context.Background(), client, arguments)
}

func ensureObservationCertificateBootstrap(ctx context.Context, client kubernetes.Interface, arguments []string) error {
	flags := flag.NewFlagSet("bootstrap-observation-certificates", flag.ContinueOnError)
	var namespace, release, bootstrapID string
	flags.StringVar(&namespace, "namespace", "", "Waycloak installation namespace")
	flags.StringVar(&release, "release", "", "Helm release name")
	flags.StringVar(&bootstrapID, "bootstrap-id", "", "exact GitOps bootstrap identity")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || namespace == "" || release == "" || bootstrapID == "" {
		return errors.New("namespace, release, and bootstrap-id are required")
	}
	if _, _, err := waycloakctl.BootstrapObservationSecrets(ctx, client, namespace, release, bootstrapID); err != nil {
		return err
	}
	return nil
}

func waitForControllerReady(arguments []string) error {
	flags := flag.NewFlagSet("wait-ready", flag.ContinueOnError)
	var endpoint string
	var timeout time.Duration
	flags.StringVar(&endpoint, "url", "", "controller readiness URL")
	flags.DurationVar(&timeout, "timeout", 2*time.Minute, "maximum readiness wait")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.Path != "/readyz" || flags.NArg() != 0 || timeout <= 0 || timeout > 10*time.Minute {
		return errors.New("an HTTP /readyz URL and timeout from 1ns through 10m are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if requestErr != nil {
			return requestErr
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("controller did not become ready: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
