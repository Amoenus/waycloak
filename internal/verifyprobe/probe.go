// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package verifyprobe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	maximumResponseBytes = 128
	maximumSuccessHold   = 30 * time.Second
)

func Run(ctx context.Context, getenv func(string) string) error {
	if getenv == nil {
		return errors.New("environment reader is required")
	}
	holdAfterSuccess := time.Duration(0)
	if raw := getenv("PROBE_HOLD_AFTER_SUCCESS"); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil || parsed <= 0 || parsed > maximumSuccessHold {
			return errors.New("PROBE_HOLD_AFTER_SUCCESS must be a positive duration no greater than 30s")
		}
		holdAfterSuccess = parsed
	}
	probeURL := getenv("PROBE_URL")
	parsed, err := url.Parse(probeURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("PROBE_URL must be an absolute HTTPS URL without user information or a fragment")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile := getenv("PROBE_CA_FILE"); caFile != "" {
		contents, readErr := os.ReadFile(caFile)
		if readErr != nil {
			return fmt.Errorf("read probe CA: %w", readErr)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(contents) {
			return errors.New("probe CA contains no PEM certificates")
		}
		tlsConfig.RootCAs = roots
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: tlsConfig}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("query egress observer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumResponseBytes))
		return fmt.Errorf("egress observer returned HTTP %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read egress observer: %w", err)
	}
	if len(contents) > maximumResponseBytes {
		return errors.New("egress observer response exceeds 128 bytes")
	}
	address := strings.TrimSpace(string(contents))
	if _, err := netip.ParseAddr(address); err != nil {
		return errors.New("egress observer did not return one IP address")
	}
	terminationPath := getenv("TERMINATION_LOG_PATH")
	if terminationPath == "" {
		terminationPath = "/dev/termination-log"
	}
	if err := os.WriteFile(terminationPath, []byte(address), 0o600); err != nil {
		return fmt.Errorf("write termination result: %w", err)
	}
	if holdAfterSuccess > 0 {
		timer := time.NewTimer(holdAfterSuccess)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
