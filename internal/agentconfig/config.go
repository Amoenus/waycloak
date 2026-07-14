// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package agentconfig

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Amoenus/waycloak/internal/contract"
	"github.com/Amoenus/waycloak/internal/dataplane"
)

const DefaultDirectory = "/run/waycloak"

func Load(directory string) (dataplane.Config, error) {
	if directory == "" {
		directory = DefaultDirectory
	}
	read := func(name string) (string, error) {
		value, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return "", fmt.Errorf("read allocation field %q: %w", name, err)
		}
		return strings.TrimSpace(string(value)), nil
	}
	version, err := read("version")
	if err != nil {
		return dataplane.Config{}, err
	}
	if version != contract.AllocationVersion {
		return dataplane.Config{}, fmt.Errorf("allocation version %q is unsupported", version)
	}
	podUID, err := read("podUID")
	if err != nil {
		return dataplane.Config{}, err
	}
	addressText, err := read("address")
	if err != nil {
		return dataplane.Config{}, err
	}
	address, err := netip.ParseAddr(addressText)
	if err != nil {
		return dataplane.Config{}, fmt.Errorf("parse allocated address: %w", err)
	}
	overlayText, err := read("overlayCIDR")
	if err != nil {
		return dataplane.Config{}, err
	}
	overlay, err := netip.ParsePrefix(overlayText)
	if err != nil {
		return dataplane.Config{}, fmt.Errorf("parse overlay CIDR: %w", err)
	}
	gatewayText, err := read("gatewayAddress")
	if err != nil {
		return dataplane.Config{}, err
	}
	gateway, err := netip.ParseAddr(gatewayText)
	if err != nil {
		return dataplane.Config{}, fmt.Errorf("parse gateway address: %w", err)
	}
	endpointText, err := read("gatewayEndpoint")
	if err != nil {
		return dataplane.Config{}, err
	}
	endpoint, err := netip.ParseAddrPort(endpointText)
	if err != nil {
		return dataplane.Config{}, fmt.Errorf("parse observed gateway endpoint: %w", err)
	}
	healthPort, err := readUint32(read, "gatewayHealthPort")
	if err != nil {
		return dataplane.Config{}, err
	}
	if healthPort > 65535 {
		return dataplane.Config{}, errors.New("gateway health port must fit in a TCP port")
	}
	vni, err := readUint32(read, "vni")
	if err != nil {
		return dataplane.Config{}, err
	}
	mtu, err := readInt(read, "mtu")
	if err != nil {
		return dataplane.Config{}, err
	}
	mode, err := read("clusterTrafficMode")
	if err != nil {
		return dataplane.Config{}, err
	}
	cfg := dataplane.Config{PodUID: podUID, Address: netip.PrefixFrom(address, overlay.Bits()), OverlayCIDR: overlay, GatewayAddress: gateway, GatewayEndpoint: endpoint, GatewayHealthPort: uint16(healthPort), VNI: vni, MTU: mtu, ClusterTrafficMode: dataplane.ClusterTrafficMode(mode)}
	if underlay, readErr := read("underlayInterface"); readErr == nil {
		cfg.UnderlayInterface = underlay
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return dataplane.Config{}, readErr
	}
	if cidrs, readErr := read("clusterCIDRs"); readErr == nil && cidrs != "" {
		for _, value := range strings.FieldsFunc(cidrs, func(r rune) bool { return r == ',' || r == '\n' }) {
			prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(value))
			if parseErr != nil {
				return dataplane.Config{}, fmt.Errorf("parse cluster CIDR %q: %w", value, parseErr)
			}
			cfg.ClusterCIDRs = append(cfg.ClusterCIDRs, prefix)
		}
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return dataplane.Config{}, readErr
	}
	return cfg, cfg.Validate()
}

func readUint32(read func(string) (string, error), name string) (uint32, error) {
	value, err := read(name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse allocation field %q: %w", name, err)
	}
	return uint32(parsed), nil
}

func readInt(read func(string) (string, error), name string) (int, error) {
	value, err := read(name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse allocation field %q: %w", name, err)
	}
	return parsed, nil
}
