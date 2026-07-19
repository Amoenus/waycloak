// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package proton

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"time"

	"github.com/Amoenus/waycloak/internal/provider"
)

const (
	DefaultGatewayPort = "5351"
	requestedLifetime  = 60 * time.Second
	renewalFraction    = 0.75
	defaultTimeout     = 250 * time.Millisecond
	defaultAttempts    = 4

	opExternalAddress byte = 0
	opUDPMapping      byte = 1
	opTCPMapping      byte = 2
)

var (
	ErrInvalidLeaseRequest = errors.New("invalid Proton NAT-PMP lease request")
	ErrSharedPortMismatch  = errors.New("proton NAT-PMP returned different TCP and UDP ports")
)

type DialFunc func(context.Context, string, string) (net.Conn, error)
type GatewayResolver func(string) (string, error)

// Client implements Proton's documented 60-second NAT-PMP lease loop. The
// caller owns durable internal-port allocation and lease generations.
type Client struct {
	GatewayAddress  string
	TunnelInterface string
	Timeout         time.Duration
	Attempts        int
	Dial            DialFunc
	ResolveGateway  GatewayResolver
	Now             func() time.Time
}

func New(tunnelInterface string) *Client {
	return &Client{TunnelInterface: tunnelInterface}
}

func (client *Client) ObserveCapabilities(ctx context.Context) (provider.PortForwardCapabilities, error) {
	if _, err := client.externalAddress(ctx); err != nil {
		return provider.PortForwardCapabilities{}, err
	}
	return provider.PortForwardCapabilities{
		Protocols:             []provider.PortForwardProtocol{provider.ProtocolTCP, provider.ProtocolUDP},
		MaxLeases:             0,
		SharedPort:            true,
		SupportsRequestedPort: false,
		MinimumLeaseDuration:  requestedLifetime,
	}, nil
}

func (client *Client) EnsureLease(ctx context.Context, request provider.PortForwardLeaseRequest) (provider.PortForwardLeaseObservation, error) {
	protocols, err := validateRequest(request)
	if err != nil {
		return provider.PortForwardLeaseObservation{}, err
	}
	suggestedPort := request.SuggestedExternalPort
	var externalPort uint16
	var lifetime uint32
	for _, protocol := range protocols {
		mapping, mappingErr := client.mapPort(ctx, protocol, request.InternalPort, suggestedPort, uint32(requestedLifetime/time.Second))
		if mappingErr != nil {
			return provider.PortForwardLeaseObservation{}, mappingErr
		}
		if externalPort != 0 && mapping.externalPort != externalPort {
			return provider.PortForwardLeaseObservation{}, ErrSharedPortMismatch
		}
		externalPort = mapping.externalPort
		suggestedPort = externalPort
		if lifetime == 0 || mapping.lifetime < lifetime {
			lifetime = mapping.lifetime
		}
	}
	if externalPort == 0 || lifetime == 0 {
		return provider.PortForwardLeaseObservation{}, errors.New("proton NAT-PMP returned an empty lease")
	}
	publicAddress, err := client.externalAddress(ctx)
	if err != nil {
		return provider.PortForwardLeaseObservation{}, err
	}
	issuedAt := client.now()
	duration := time.Duration(lifetime) * time.Second
	return provider.PortForwardLeaseObservation{
		PublicAddress: publicAddress,
		PublicPort:    externalPort,
		IssuedAt:      issuedAt,
		RenewAfter:    issuedAt.Add(time.Duration(float64(duration) * renewalFraction)),
		ExpiresAt:     issuedAt.Add(duration),
	}, nil
}

func (client *Client) externalAddress(ctx context.Context) (netip.Addr, error) {
	response, err := client.transact(ctx, []byte{0, opExternalAddress}, opExternalAddress, 12)
	if err != nil {
		return netip.Addr{}, err
	}
	address := netip.AddrFrom4([4]byte(response[8:12]))
	if !address.IsGlobalUnicast() {
		return netip.Addr{}, errors.New("proton NAT-PMP returned an invalid public address")
	}
	return address, nil
}

func (client *Client) ReleaseLease(ctx context.Context, request provider.PortForwardLeaseRequest) error {
	protocols, err := validateRequest(request)
	if err != nil {
		return err
	}
	var releaseErr error
	for _, protocol := range protocols {
		_, err := client.mapPort(ctx, protocol, request.InternalPort, request.SuggestedExternalPort, 0)
		releaseErr = errors.Join(releaseErr, err)
	}
	return releaseErr
}

type mappingResponse struct {
	externalPort uint16
	lifetime     uint32
}

func (client *Client) mapPort(ctx context.Context, protocol provider.PortForwardProtocol, internalPort, externalPort uint16, lifetime uint32) (mappingResponse, error) {
	opcode := opUDPMapping
	if protocol == provider.ProtocolTCP {
		opcode = opTCPMapping
	}
	request := make([]byte, 12)
	request[1] = opcode
	binary.BigEndian.PutUint16(request[4:6], internalPort)
	binary.BigEndian.PutUint16(request[6:8], externalPort)
	binary.BigEndian.PutUint32(request[8:12], lifetime)
	response, err := client.transact(ctx, request, opcode, 16)
	if err != nil {
		return mappingResponse{}, err
	}
	if binary.BigEndian.Uint16(response[8:10]) != internalPort {
		return mappingResponse{}, errors.New("proton NAT-PMP response internal port does not match request")
	}
	mapping := mappingResponse{externalPort: binary.BigEndian.Uint16(response[10:12]), lifetime: binary.BigEndian.Uint32(response[12:16])}
	if lifetime == 0 && mapping.lifetime != 0 {
		return mappingResponse{}, errors.New("proton NAT-PMP did not confirm mapping release")
	}
	return mapping, nil
}

func (client *Client) transact(ctx context.Context, request []byte, opcode byte, responseLength int) ([]byte, error) {
	gatewayAddress, err := client.gatewayAddress()
	if err != nil {
		return nil, err
	}
	connection, err := client.dial()(ctx, "udp4", gatewayAddress)
	if err != nil {
		return nil, fmt.Errorf("connect to Proton NAT-PMP gateway: %w", err)
	}
	defer connection.Close()
	timeout := client.timeout()
	buffer := make([]byte, 32)
	var lastErr error
	for attempt := 0; attempt < client.attempts(); attempt++ {
		deadline := time.Now().Add(timeout << attempt)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("set Proton NAT-PMP deadline: %w", err)
		}
		if _, err := connection.Write(request); err != nil {
			lastErr = err
			continue
		}
		length, err := connection.Read(buffer)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if length != responseLength {
			return nil, fmt.Errorf("invalid Proton NAT-PMP response length %d", length)
		}
		response := slices.Clone(buffer[:length])
		if response[0] != 0 || response[1] != opcode|0x80 {
			return nil, errors.New("invalid Proton NAT-PMP response header")
		}
		if result := binary.BigEndian.Uint16(response[2:4]); result != 0 {
			return nil, resultError(result)
		}
		return response, nil
	}
	return nil, fmt.Errorf("proton NAT-PMP request timed out after %d attempts: %w", client.attempts(), lastErr)
}

func validateRequest(request provider.PortForwardLeaseRequest) ([]provider.PortForwardProtocol, error) {
	if request.Identity == "" || request.InternalPort == 0 || len(request.Protocols) == 0 {
		return nil, ErrInvalidLeaseRequest
	}
	protocols := slices.Clone(request.Protocols)
	slices.Sort(protocols)
	protocols = slices.Compact(protocols)
	for _, protocol := range protocols {
		if protocol != provider.ProtocolTCP && protocol != provider.ProtocolUDP {
			return nil, ErrInvalidLeaseRequest
		}
	}
	return protocols, nil
}

func resultError(code uint16) error {
	description := map[uint16]string{1: "unsupported version", 2: "not authorized", 3: "network failure", 4: "out of resources", 5: "unsupported operation"}[code]
	if description == "" {
		description = "unknown failure"
	}
	return fmt.Errorf("proton NAT-PMP result %d: %s", code, description)
}

func (client *Client) gatewayAddress() (string, error) {
	if client.GatewayAddress != "" {
		return client.GatewayAddress, nil
	}
	resolver := client.ResolveGateway
	if resolver == nil {
		resolver = resolveTunnelGateway
	}
	address, err := resolver(client.TunnelInterface)
	if err != nil {
		return "", fmt.Errorf("resolve Proton NAT-PMP gateway: %w", err)
	}
	return net.JoinHostPort(address, DefaultGatewayPort), nil
}

func resolveTunnelGateway(interfaceName string) (string, error) {
	if interfaceName == "" {
		return "", errors.New("tunnel interface is required when the NAT-PMP gateway is not overridden")
	}
	device, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return "", err
	}
	addresses, err := device.Addrs()
	if err != nil {
		return "", err
	}
	return gatewayFromAddresses(interfaceName, addresses)
}

func gatewayFromAddresses(interfaceName string, addresses []net.Addr) (string, error) {
	prefixes := make([]netip.Prefix, 0, len(addresses))
	for _, address := range addresses {
		prefix, parseErr := netip.ParsePrefix(address.String())
		if parseErr == nil && prefix.Addr().Is4() && prefix.Bits() <= 30 {
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	if len(prefixes) == 0 {
		return "", fmt.Errorf("interface %s has no usable IPv4 prefix", interfaceName)
	}
	sort.Slice(prefixes, func(i, j int) bool { return prefixes[i].String() < prefixes[j].String() })
	return prefixes[0].Addr().Next().String(), nil
}

func (client *Client) timeout() time.Duration {
	if client.Timeout > 0 {
		return client.Timeout
	}
	return defaultTimeout
}

func (client *Client) attempts() int {
	if client.Attempts > 0 {
		return client.Attempts
	}
	return defaultAttempts
}

func (client *Client) dial() DialFunc {
	if client.Dial != nil {
		return client.Dial
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialTunnel(ctx, network, address, client.TunnelInterface)
	}
}

func (client *Client) now() time.Time {
	if client.Now != nil {
		return client.Now().UTC()
	}
	return time.Now().UTC()
}
