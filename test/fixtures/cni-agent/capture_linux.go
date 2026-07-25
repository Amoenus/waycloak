// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/containernetworking/plugins/pkg/ns"
	"golang.org/x/sys/unix"
)

var captureTarget = netip.MustParseAddr("198.51.100.123")

func startPacketCapture(ctx context.Context, output string) error {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return err
	}
	var count atomic.Uint64
	if existing, err := os.ReadFile(output); err == nil {
		if value, err := strconv.ParseUint(strings.TrimSpace(string(existing)), 10, 64); err == nil {
			count.Store(value)
		}
	}
	go func() {
		defer unix.Close(fd)
		packet := make([]byte, 65535)
		for {
			length, _, err := unix.Recvfrom(fd, packet, 0)
			if err != nil {
				return
			}
			if isProbePacket(packet[:length]) {
				count.Add(1)
			}
		}
	}()
	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if err := os.WriteFile(output, []byte(fmt.Sprintf("%d\n", count.Load())), 0o600); err != nil {
		_ = unix.Close(fd)
		return err
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = unix.Shutdown(fd, unix.SHUT_RDWR)
				return
			case <-ticker.C:
				_ = os.WriteFile(output, []byte(fmt.Sprintf("%d\n", count.Load())), 0o600)
			}
		}
	}()
	return nil
}

func isProbePacket(packet []byte) bool {
	if len(packet) < 14 || binary.BigEndian.Uint16(packet[12:14]) != unix.ETH_P_IP {
		return false
	}
	ip := packet[14:]
	if len(ip) < 20 || ip[0]>>4 != 4 {
		return false
	}
	headerLength := int(ip[0]&0x0f) * 4
	if headerLength < 20 || len(ip) < headerLength+4 {
		return false
	}
	destination := netip.AddrFrom4([4]byte{ip[16], ip[17], ip[18], ip[19]})
	if destination != captureTarget || (ip[9] != unix.IPPROTO_TCP && ip[9] != unix.IPPROTO_UDP) {
		return false
	}
	port := binary.BigEndian.Uint16(ip[headerLength+2 : headerLength+4])
	return port == 18080 || port == 18081
}

func probeDirectEgress(netns string) {
	_ = ns.WithNetNSPath(netns, func(ns.NetNS) error {
		udp, err := net.DialTimeout("udp4", "198.51.100.123:18081", 20*time.Millisecond)
		if err == nil {
			_, _ = udp.Write([]byte("deny-probe"))
			_ = udp.Close()
		}
		tcp, err := net.DialTimeout("tcp4", "198.51.100.123:18080", 20*time.Millisecond)
		if err == nil {
			_ = tcp.Close()
		}
		return nil
	})
}

func htons(value uint16) uint16 { return value<<8 | value>>8 }
