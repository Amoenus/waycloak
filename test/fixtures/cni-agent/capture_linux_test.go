// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

func TestClassifyProbePacket(t *testing.T) {
	tests := []struct {
		name     string
		protocol byte
		port     uint16
		fragment uint16
		want     packetKind
	}{
		{name: "tcp", protocol: unix.IPPROTO_TCP, port: 18080, want: packetTCP},
		{name: "udp", protocol: unix.IPPROTO_UDP, port: 18081, want: packetUDP},
		{name: "dns udp", protocol: unix.IPPROTO_UDP, port: 53, want: packetDNSUDP},
		{name: "dns tcp", protocol: unix.IPPROTO_TCP, port: 53, want: packetDNSTCP},
		{name: "first fragment", protocol: unix.IPPROTO_UDP, port: 18082, fragment: 0x2000, want: packetFragment},
		{name: "later fragment", protocol: unix.IPPROTO_UDP, fragment: 1, want: packetFragment},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, ok := classifyProbePacket(probePacket(test.protocol, test.port, test.fragment))
			if !ok || kind != test.want {
				t.Fatalf("classification = %q, %v; want %q, true", kind, ok, test.want)
			}
		})
	}
	if kind, ok := classifyProbePacket(probePacket(unix.IPPROTO_TCP, 443, 0)); ok {
		t.Fatalf("unrelated packet classified as %q", kind)
	}
}

func probePacket(protocol byte, port, fragment uint16) []byte {
	packet := make([]byte, 14+20+4)
	binary.BigEndian.PutUint16(packet[12:14], unix.ETH_P_IP)
	ip := packet[14:]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[6:8], fragment)
	ip[9] = protocol
	copy(ip[16:20], captureTarget.AsSlice())
	binary.BigEndian.PutUint16(ip[22:24], port)
	return packet
}
