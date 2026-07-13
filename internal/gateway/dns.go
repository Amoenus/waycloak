// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
)

type DNSService interface {
	Reconcile(context.Context, DesiredState) error
}

type ResolverConfig struct {
	ClusterUpstream netip.AddrPort
	ClusterZones    []string
}

func ResolverConfigFromFile(path string) (ResolverConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return ResolverConfig{}, fmt.Errorf("open cluster resolver configuration: %w", err)
	}
	defer file.Close()
	var config ResolverConfig
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(strings.SplitN(scanner.Text(), "#", 2)[0])
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			if config.ClusterUpstream.IsValid() {
				continue
			}
			address, parseErr := netip.ParseAddr(fields[1])
			if parseErr == nil && !address.IsLoopback() && !address.IsUnspecified() {
				config.ClusterUpstream = netip.AddrPortFrom(address, DNSPort)
			}
		case "search", "domain":
			for _, zone := range fields[1:] {
				zone = normalizeDNSName(zone)
				if zone != "." {
					config.ClusterZones = appendUnique(config.ClusterZones, zone)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ResolverConfig{}, fmt.Errorf("read cluster resolver configuration: %w", err)
	}
	if !config.ClusterUpstream.IsValid() {
		return ResolverConfig{}, errors.New("cluster resolver configuration has no non-loopback nameserver")
	}
	if len(config.ClusterZones) == 0 {
		return ResolverConfig{}, errors.New("cluster resolver configuration has no search domains")
	}
	return config, nil
}

func normalizeDNSName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "." {
		return value
	}
	return strings.TrimSuffix(value, ".") + "."
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
