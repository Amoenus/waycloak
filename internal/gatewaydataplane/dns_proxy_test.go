// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestSplitDNSProxyPreservesProtocolAndContainsSearchQueries(t *testing.T) {
	cluster := startDNSFixture(t, false)
	external := startDNSFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := testConfig()
	config.GatewayAddress = netip.MustParseAddr("127.0.0.1")
	config.ClusterDNSUpstream = cluster.address
	config.DNSUpstream = external.address
	proxy, err := startDNSProxy(ctx, config)
	if err != nil {
		t.Fatal(err)
	}

	queries := []struct {
		name, network string
		typeCode      dnsmessage.Type
	}{
		{"kubernetes.default.svc.cluster.local.", "udp4", dnsmessage.TypeA},
		{"kubernetes.default.svc.cluster.local.", "tcp4", dnsmessage.TypeA},
		{"api.ipify.org.applications-media.svc.cluster.local.", "udp4", dnsmessage.TypeA},
		{"example.com.", "udp4", dnsmessage.TypeA},
		{"example.com.", "udp4", dnsmessage.TypeAAAA},
		{"example.com.", "tcp4", dnsmessage.TypeA},
	}
	var wait sync.WaitGroup
	for _, query := range queries {
		query := query
		wait.Add(1)
		go func() {
			defer wait.Done()
			request, identity, buildErr := proxy.probeRequest(query.name, query.typeCode)
			if buildErr != nil {
				t.Errorf("build %s: %v", query.name, buildErr)
				return
			}
			var response []byte
			if query.network == "udp4" {
				response, buildErr = exchangeDNSUDP(ctx, dnsListenAddress(config), request)
			} else {
				response, buildErr = exchangeDNSTCP(ctx, dnsListenAddress(config), request)
			}
			if buildErr != nil {
				t.Errorf("exchange %s/%s: %v", query.network, query.name, buildErr)
				return
			}
			if _, buildErr = validateDNSResponse(response, identity); buildErr != nil {
				t.Errorf("validate %s/%s: %v", query.network, query.name, buildErr)
			}
		}()
	}
	wait.Wait()
	if !cluster.saw("udp4", "api.ipify.org.applications-media.svc.cluster.local.") || external.sawAny("api.ipify.org.applications-media.svc.cluster.local.") {
		t.Fatal("Kubernetes search expansion escaped to the external VPN resolver")
	}
	if !cluster.saw("udp4", "kubernetes.default.svc.cluster.local.") || !cluster.saw("tcp4", "kubernetes.default.svc.cluster.local.") {
		t.Fatal("cluster-local UDP/TCP did not use the reviewed cluster resolver")
	}
	if !external.saw("udp4", "example.com.") || !external.saw("tcp4", "example.com.") || cluster.sawAny("example.com.") {
		t.Fatal("external UDP/TCP did not remain exclusive to the VPN resolver")
	}
	if !cluster.edns() || !external.edns() {
		t.Fatal("EDNS0 was not preserved through both split paths")
	}

	largeRequest, largeIdentity, err := proxy.probeRequest("large.example.", dnsmessage.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	largeResponse, err := exchangeDNSUDP(ctx, dnsListenAddress(config), largeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(largeResponse) <= 1500 {
		t.Fatalf("fragmentation-sized DNS response = %d bytes", len(largeResponse))
	}
	if _, err := validateDNSResponse(largeResponse, largeIdentity); err != nil {
		t.Fatal(err)
	}

	if err := proxy.probe(ctx, "udp4", "truncated.example.", dnsmessage.TypeA); err != nil {
		t.Fatalf("truncated UDP did not retry through TCP: %v", err)
	}
	if !external.saw("udp4", "truncated.example.") || !external.saw("tcp4", "truncated.example.") {
		t.Fatal("truncation did not produce one external TCP retry")
	}
}

type dnsFixture struct {
	address  netip.AddrPort
	large    bool
	mu       sync.Mutex
	queries  map[string]int
	sawEDNS0 bool
}

func startDNSFixture(t *testing.T, large bool) *dnsFixture {
	t.Helper()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	tcp, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		_ = udp.Close()
		t.Fatal(err)
	}
	fixture := &dnsFixture{address: netip.MustParseAddrPort(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))), large: large, queries: map[string]int{}}
	t.Cleanup(func() { _ = udp.Close(); _ = tcp.Close() })
	go fixture.serveUDP(udp)
	go fixture.serveTCP(tcp)
	return fixture
}

func (fixture *dnsFixture) serveUDP(listener net.PacketConn) {
	buffer := make([]byte, maximumDNSMessage)
	for {
		count, peer, err := listener.ReadFrom(buffer)
		if err != nil {
			return
		}
		request := append([]byte(nil), buffer[:count]...)
		response, err := fixture.response("udp4", request)
		if err == nil {
			_, _ = listener.WriteTo(response, peer)
		}
	}
}

func (fixture *dnsFixture) serveTCP(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			request, err := readDNSFrame(connection)
			if err != nil {
				return
			}
			response, err := fixture.response("tcp4", request)
			if err == nil {
				_ = writeDNSFrame(connection, response)
			}
		}()
	}
}

func (fixture *dnsFixture) response(network string, request []byte) ([]byte, error) {
	var query dnsmessage.Message
	if err := query.Unpack(request); err != nil || len(query.Questions) != 1 {
		return nil, errors.New("invalid fixture query")
	}
	name := query.Questions[0].Name.String()
	fixture.mu.Lock()
	fixture.queries[network+"|"+name]++
	for _, additional := range query.Additionals {
		if additional.Header.Type == dnsmessage.TypeOPT {
			fixture.sawEDNS0 = true
		}
	}
	fixture.mu.Unlock()
	response := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionDesired: query.RecursionDesired, RecursionAvailable: true}, Questions: query.Questions}
	if network == "udp4" && name == "truncated.example." {
		response.Truncated = true
		return response.Pack()
	}
	question := query.Questions[0]
	header := dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET, TTL: 30}
	switch question.Type {
	case dnsmessage.TypeA:
		header.Type = dnsmessage.TypeA
		response.Answers = append(response.Answers, dnsmessage.Resource{Header: header, Body: &dnsmessage.AResource{A: [4]byte{192, 0, 2, 10}}})
	case dnsmessage.TypeAAAA:
		header.Type = dnsmessage.TypeAAAA
		response.Answers = append(response.Answers, dnsmessage.Resource{Header: header, Body: &dnsmessage.AAAAResource{AAAA: [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}}})
	}
	if fixture.large && name == "large.example." {
		header.Type = dnsmessage.TypeTXT
		for index := 0; index < 10; index++ {
			response.Answers = append(response.Answers, dnsmessage.Resource{Header: header, Body: &dnsmessage.TXTResource{TXT: []string{strings.Repeat("x", 200)}}})
		}
	}
	return response.Pack()
}

func (fixture *dnsFixture) saw(network, name string) bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.queries[network+"|"+name] > 0
}

func (fixture *dnsFixture) sawAny(name string) bool {
	return fixture.saw("udp4", name) || fixture.saw("tcp4", name)
}

func (fixture *dnsFixture) edns() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.sawEDNS0
}
