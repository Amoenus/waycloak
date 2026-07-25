// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/containernetworking/plugins/pkg/ns"
	"golang.org/x/sys/unix"
)

var captureTarget = netip.MustParseAddr("198.51.100.123")

type packetKind string

const (
	packetTCP      packetKind = "tcp"
	packetUDP      packetKind = "udp"
	packetDNSUDP   packetKind = "dnsUDP"
	packetDNSTCP   packetKind = "dnsTCP"
	packetFragment packetKind = "fragment"
)

type captureCounts struct {
	TCP      uint64 `json:"tcp"`
	UDP      uint64 `json:"udp"`
	DNSUDP   uint64 `json:"dnsUDP"`
	DNSTCP   uint64 `json:"dnsTCP"`
	Fragment uint64 `json:"fragment"`
}

type atomicCaptureCounts struct {
	tcp      atomic.Uint64
	udp      atomic.Uint64
	dnsUDP   atomic.Uint64
	dnsTCP   atomic.Uint64
	fragment atomic.Uint64
}

func startPacketCapture(ctx context.Context, output string) error {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return err
	}
	var counts atomicCaptureCounts
	if existing, err := os.ReadFile(output); err == nil {
		var value captureCounts
		if json.Unmarshal(existing, &value) == nil {
			counts.store(value)
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
			if kind, ok := classifyProbePacket(packet[:length]); ok {
				counts.increment(kind)
			}
		}
	}()
	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if err := writeCaptureCounts(output, counts.load()); err != nil {
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
				_ = writeCaptureCounts(output, counts.load())
			}
		}
	}()
	return nil
}

func classifyProbePacket(packet []byte) (packetKind, bool) {
	if len(packet) < 14 || binary.BigEndian.Uint16(packet[12:14]) != unix.ETH_P_IP {
		return "", false
	}
	ip := packet[14:]
	if len(ip) < 20 || ip[0]>>4 != 4 || netip.AddrFrom4([4]byte{ip[16], ip[17], ip[18], ip[19]}) != captureTarget {
		return "", false
	}
	if binary.BigEndian.Uint16(ip[6:8])&0x3fff != 0 && ip[9] == unix.IPPROTO_UDP {
		return packetFragment, true
	}
	headerLength := int(ip[0]&0x0f) * 4
	if headerLength < 20 || len(ip) < headerLength+4 {
		return "", false
	}
	port := binary.BigEndian.Uint16(ip[headerLength+2 : headerLength+4])
	switch {
	case ip[9] == unix.IPPROTO_TCP && port == 18080:
		return packetTCP, true
	case ip[9] == unix.IPPROTO_UDP && port == 18081:
		return packetUDP, true
	case ip[9] == unix.IPPROTO_UDP && port == 53:
		return packetDNSUDP, true
	case ip[9] == unix.IPPROTO_TCP && port == 53:
		return packetDNSTCP, true
	default:
		return "", false
	}
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
		dnsUDP, err := net.DialTimeout("udp4", "198.51.100.123:53", 20*time.Millisecond)
		if err == nil {
			_, _ = dnsUDP.Write([]byte("dns-deny-probe"))
			_ = dnsUDP.Close()
		}
		dnsTCP, err := net.DialTimeout("tcp4", "198.51.100.123:53", 20*time.Millisecond)
		if err == nil {
			_ = dnsTCP.Close()
		}
		fragment, err := net.DialTimeout("udp4", "198.51.100.123:18082", 20*time.Millisecond)
		if err == nil {
			_, _ = fragment.Write(make([]byte, 4096))
			_ = fragment.Close()
		}
		return nil
	})
}

func (c *atomicCaptureCounts) increment(kind packetKind) {
	switch kind {
	case packetTCP:
		c.tcp.Add(1)
	case packetUDP:
		c.udp.Add(1)
	case packetDNSUDP:
		c.dnsUDP.Add(1)
	case packetDNSTCP:
		c.dnsTCP.Add(1)
	case packetFragment:
		c.fragment.Add(1)
	}
}

func (c *atomicCaptureCounts) store(value captureCounts) {
	c.tcp.Store(value.TCP)
	c.udp.Store(value.UDP)
	c.dnsUDP.Store(value.DNSUDP)
	c.dnsTCP.Store(value.DNSTCP)
	c.fragment.Store(value.Fragment)
}

func (c *atomicCaptureCounts) load() captureCounts {
	return captureCounts{TCP: c.tcp.Load(), UDP: c.udp.Load(), DNSUDP: c.dnsUDP.Load(), DNSTCP: c.dnsTCP.Load(), Fragment: c.fragment.Load()}
}

func writeCaptureCounts(output string, counts captureCounts) error {
	encoded, err := json.Marshal(counts)
	if err != nil {
		return err
	}
	temporary := output + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, output)
}

func htons(value uint16) uint16 { return value<<8 | value>>8 }
