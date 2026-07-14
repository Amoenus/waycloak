// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package proton

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

func TestEnsureLeaseMapsTCPAndUDPToOnePort(t *testing.T) {
	server := newNATPMPServer(t, func(request []byte, count int) []byte {
		if count == 1 && request[1] != opTCPMapping || count == 2 && request[1] != opUDPMapping {
			t.Errorf("opcode on request %d = %d", count, request[1])
		}
		if got := binary.BigEndian.Uint16(request[4:6]); got != 7 {
			t.Errorf("internal port = %d", got)
		}
		suggested := binary.BigEndian.Uint16(request[6:8])
		if count == 1 && suggested != 0 || count == 2 && suggested != 42000 {
			t.Errorf("suggested port on request %d = %d", count, suggested)
		}
		return mappingReply(request, 42000, 60, 0)
	})
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	client := testClient(server, now)
	observation, err := client.EnsureLease(context.Background(), provider.PortForwardLeaseRequest{Identity: "lease-uid", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolUDP, provider.ProtocolTCP}})
	if err != nil {
		t.Fatal(err)
	}
	if observation.PublicPort != 42000 || !observation.IssuedAt.Equal(now) || !observation.RenewAfter.Equal(now.Add(45*time.Second)) || !observation.ExpiresAt.Equal(now.Add(60*time.Second)) {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestEnsureLeaseRejectsSplitProtocolPorts(t *testing.T) {
	server := newNATPMPServer(t, func(request []byte, count int) []byte {
		return mappingReply(request, uint16(42000+count-1), 60, 0)
	})
	client := testClient(server, time.Now())
	_, err := client.EnsureLease(context.Background(), provider.PortForwardLeaseRequest{Identity: "lease-uid", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}})
	if !errors.Is(err, ErrSharedPortMismatch) {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureLeaseAcceptsProviderPortRotation(t *testing.T) {
	server := newNATPMPServer(t, func(request []byte, count int) []byte {
		if count == 1 && binary.BigEndian.Uint16(request[6:8]) != 41000 {
			t.Errorf("first renewal suggestion = %d", binary.BigEndian.Uint16(request[6:8]))
		}
		if count == 2 && binary.BigEndian.Uint16(request[6:8]) != 42000 {
			t.Errorf("paired-protocol suggestion = %d", binary.BigEndian.Uint16(request[6:8]))
		}
		return mappingReply(request, 42000, 60, 0)
	})
	client := testClient(server, time.Now())
	observation, err := client.EnsureLease(context.Background(), provider.PortForwardLeaseRequest{Identity: "lease-uid", InternalPort: 7, SuggestedExternalPort: 41000, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}})
	if err != nil || observation.PublicPort != 42000 {
		t.Fatalf("observation=%#v error=%v", observation, err)
	}
}

func TestObserveCapabilitiesRequiresSuccessfulProbe(t *testing.T) {
	server := newNATPMPServer(t, func(request []byte, _ int) []byte {
		response := make([]byte, 12)
		response[1] = request[1] | 0x80
		copy(response[8:12], net.ParseIP("203.0.113.10").To4())
		return response
	})
	client := testClient(server, time.Now())
	capabilities, err := client.ObserveCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.MaxLeases != 0 || !capabilities.SharedPort || capabilities.SupportsRequestedPort || len(capabilities.Protocols) != 2 || capabilities.MinimumLeaseDuration != 60*time.Second {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestClientRetriesAndReturnsTypedProviderFailure(t *testing.T) {
	server := newNATPMPServer(t, func(request []byte, count int) []byte {
		if count == 1 {
			return nil
		}
		return mappingReply(request, 0, 0, 2)
	})
	client := testClient(server, time.Now())
	client.Timeout = 10 * time.Millisecond
	_, err := client.EnsureLease(context.Background(), provider.PortForwardLeaseRequest{Identity: "lease-uid", InternalPort: 7, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP}})
	if err == nil || err.Error() != "proton NAT-PMP result 2: not authorized" {
		t.Fatalf("error = %v", err)
	}
}

func TestReleaseLeaseSendsZeroLifetimeForEveryProtocol(t *testing.T) {
	server := newNATPMPServer(t, func(request []byte, _ int) []byte {
		if lifetime := binary.BigEndian.Uint32(request[8:12]); lifetime != 0 {
			t.Errorf("release lifetime = %d", lifetime)
		}
		return mappingReply(request, 42000, 0, 0)
	})
	client := testClient(server, time.Now())
	err := client.ReleaseLease(context.Background(), provider.PortForwardLeaseRequest{Identity: "lease-uid", InternalPort: 7, SuggestedExternalPort: 42000, Protocols: []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInvalidLeaseRequestNeverUsesNetwork(t *testing.T) {
	client := &Client{Dial: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("network used for invalid request")
		return nil, nil
	}}
	_, err := client.EnsureLease(context.Background(), provider.PortForwardLeaseRequest{})
	if !errors.Is(err, ErrInvalidLeaseRequest) {
		t.Fatalf("error = %v", err)
	}
}

type natpmpServer struct {
	address string
	close   func()
}

func newNATPMPServer(t *testing.T, response func([]byte, int) []byte) natpmpServer {
	t.Helper()
	connection, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		buffer := make([]byte, 32)
		count := 0
		for {
			_ = connection.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			length, peer, readErr := connection.ReadFrom(buffer)
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			count++
			reply := response(append([]byte(nil), buffer[:length]...), count)
			if reply != nil {
				_, _ = connection.WriteTo(reply, peer)
			}
		}
	}()
	cleanup := func() {
		once.Do(func() {
			cancel()
			_ = connection.Close()
			<-closed
		})
	}
	t.Cleanup(cleanup)
	return natpmpServer{address: connection.LocalAddr().String(), close: cleanup}
}

func testClient(server natpmpServer, now time.Time) *Client {
	return &Client{
		GatewayAddress: server.address,
		Attempts:       3,
		Timeout:        50 * time.Millisecond,
		Now:            func() time.Time { return now },
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
}

func TestGatewayAddressUsesExplicitOverride(t *testing.T) {
	client := &Client{GatewayAddress: "192.0.2.1:5351", ResolveGateway: func(string) (string, error) {
		t.Fatal("resolver called for explicit override")
		return "", nil
	}}
	address, err := client.gatewayAddress()
	if err != nil || address != "192.0.2.1:5351" {
		t.Fatalf("address=%q error=%v", address, err)
	}
}

func TestGatewayAddressDerivesTunnelSubnetPeer(t *testing.T) {
	client := &Client{TunnelInterface: "tunwaycloak", ResolveGateway: func(interfaceName string) (string, error) {
		if interfaceName != "tunwaycloak" {
			t.Fatalf("interface = %q", interfaceName)
		}
		return "10.16.0.1", nil
	}}
	address, err := client.gatewayAddress()
	if err != nil || address != "10.16.0.1:5351" {
		t.Fatalf("address=%q error=%v", address, err)
	}
}

func TestGatewayFromAddressesSelectsIPv4SubnetPeer(t *testing.T) {
	addresses := []net.Addr{
		&net.IPNet{IP: net.ParseIP("2001:db8::7"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("10.16.0.7"), Mask: net.CIDRMask(16, 32)},
	}
	address, err := gatewayFromAddresses("tunwaycloak", addresses)
	if err != nil || address != "10.16.0.1" {
		t.Fatalf("address=%q error=%v", address, err)
	}
}

func TestGatewayFromAddressesRejectsMissingUsablePrefix(t *testing.T) {
	addresses := []net.Addr{&net.IPNet{IP: net.ParseIP("10.16.0.7"), Mask: net.CIDRMask(32, 32)}}
	if _, err := gatewayFromAddresses("tunwaycloak", addresses); err == nil {
		t.Fatal("missing usable prefix was accepted")
	}
}

func mappingReply(request []byte, externalPort uint16, lifetime uint32, result uint16) []byte {
	response := make([]byte, 16)
	response[1] = request[1] | 0x80
	binary.BigEndian.PutUint16(response[2:4], result)
	copy(response[8:10], request[4:6])
	binary.BigEndian.PutUint16(response[10:12], externalPort)
	binary.BigEndian.PutUint32(response[12:16], lifetime)
	return response
}
