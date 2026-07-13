// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const maxDNSMessageSize = 65535

type DNSProxy struct {
	ClusterUpstream  netip.AddrPort
	ExternalUpstream netip.AddrPort
	ClusterZones     []string
	Port             uint16

	mu       sync.Mutex
	address  string
	udp      net.PacketConn
	tcp      net.Listener
	serveErr chan error
}

func NewDNSProxyFromResolvConf(path string) (*DNSProxy, error) {
	config, err := ResolverConfigFromFile(path)
	if err != nil {
		return nil, err
	}
	return &DNSProxy{ClusterUpstream: config.ClusterUpstream, ExternalUpstream: netip.MustParseAddrPort("127.0.0.1:53"), ClusterZones: config.ClusterZones, Port: DNSPort}, nil
}

func (proxy *DNSProxy) Reconcile(ctx context.Context, desired DesiredState) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	if !proxy.ClusterUpstream.IsValid() || !proxy.ExternalUpstream.IsValid() || proxy.Port == 0 || len(proxy.ClusterZones) == 0 {
		return errors.New("gateway DNS proxy configuration is invalid")
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.serveErr != nil {
		select {
		case err := <-proxy.serveErr:
			proxy.closeLocked()
			return err
		default:
		}
	}
	wanted := net.JoinHostPort(desired.GatewayAddress, fmt.Sprint(proxy.Port))
	if proxy.address == wanted && proxy.udp != nil && proxy.tcp != nil {
		return nil
	}
	proxy.closeLocked()
	udp, err := net.ListenPacket("udp", wanted)
	if err != nil {
		return fmt.Errorf("listen for gateway UDP DNS: %w", err)
	}
	tcp, err := net.Listen("tcp", wanted)
	if err != nil {
		_ = udp.Close()
		return fmt.Errorf("listen for gateway TCP DNS: %w", err)
	}
	proxy.address = wanted
	proxy.udp = udp
	proxy.tcp = tcp
	proxy.serveErr = make(chan error, 2)
	errorsChannel := proxy.serveErr
	go proxy.serveUDP(udp, errorsChannel)
	go proxy.serveTCP(tcp, errorsChannel)
	go func() {
		<-ctx.Done()
		_ = udp.Close()
		_ = tcp.Close()
	}()
	return nil
}

func (proxy *DNSProxy) closeLocked() {
	if proxy.udp != nil {
		_ = proxy.udp.Close()
	}
	if proxy.tcp != nil {
		_ = proxy.tcp.Close()
	}
	proxy.address = ""
	proxy.udp = nil
	proxy.tcp = nil
	proxy.serveErr = nil
}

func (proxy *DNSProxy) serveUDP(listener net.PacketConn, serveErr chan<- error) {
	buffer := make([]byte, maxDNSMessageSize)
	for {
		length, client, err := listener.ReadFrom(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				serveErr <- fmt.Errorf("serve gateway UDP DNS: %w", err)
			}
			return
		}
		request := append([]byte(nil), buffer[:length]...)
		go proxy.handleUDP(listener, client, request)
	}
}

func (proxy *DNSProxy) handleUDP(listener net.PacketConn, client net.Addr, request []byte) {
	upstream, err := proxy.upstreamFor(request)
	if err != nil {
		return
	}
	connection, err := net.DialTimeout("udp", upstream.String(), time.Second)
	if err != nil {
		return
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := connection.Write(request); err != nil {
		return
	}
	response := make([]byte, maxDNSMessageSize)
	length, err := connection.Read(response)
	if err == nil {
		_, _ = listener.WriteTo(response[:length], client)
	}
}

func (proxy *DNSProxy) serveTCP(listener net.Listener, serveErr chan<- error) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				serveErr <- fmt.Errorf("serve gateway TCP DNS: %w", err)
			}
			return
		}
		go proxy.handleTCP(connection)
	}
}

func (proxy *DNSProxy) handleTCP(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	for {
		header := make([]byte, 2)
		if _, err := io.ReadFull(client, header); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint16(header))
		if length == 0 || length > maxDNSMessageSize {
			return
		}
		request := make([]byte, length)
		if _, err := io.ReadFull(client, request); err != nil {
			return
		}
		upstream, err := proxy.upstreamFor(request)
		if err != nil {
			return
		}
		server, err := net.DialTimeout("tcp", upstream.String(), time.Second)
		if err != nil {
			return
		}
		_ = server.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := server.Write(append(header, request...)); err != nil {
			_ = server.Close()
			return
		}
		if _, err := io.ReadFull(server, header); err != nil {
			_ = server.Close()
			return
		}
		responseLength := int(binary.BigEndian.Uint16(header))
		if responseLength == 0 || responseLength > maxDNSMessageSize {
			_ = server.Close()
			return
		}
		response := make([]byte, responseLength)
		if _, err := io.ReadFull(server, response); err != nil {
			_ = server.Close()
			return
		}
		_ = server.Close()
		if _, err := client.Write(append(header, response...)); err != nil {
			return
		}
	}
}

func (proxy *DNSProxy) upstreamFor(message []byte) (netip.AddrPort, error) {
	var parser dnsmessage.Parser
	if _, err := parser.Start(message); err != nil {
		return netip.AddrPort{}, errors.New("malformed DNS message")
	}
	question, err := parser.Question()
	if err != nil {
		return netip.AddrPort{}, errors.New("DNS message has no question")
	}
	name := normalizeDNSName(question.Name.String())
	for _, zone := range proxy.ClusterZones {
		zone = normalizeDNSName(zone)
		if name == zone || strings.HasSuffix(name, "."+zone) {
			return proxy.ClusterUpstream, nil
		}
	}
	return proxy.ExternalUpstream, nil
}
