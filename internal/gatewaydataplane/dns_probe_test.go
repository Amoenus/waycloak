// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDNSProbeDiagnosticsDistinguishQueryTypeValidationRCodeAndLatency(t *testing.T) {
	started := time.Now().Add(-25 * time.Millisecond)
	validation := dnsProbeFailure("udp4", "example.com.", dnsmessage.TypeAAAA, "response_validation", started, "", errors.New("mismatched response"))
	for _, field := range []string{"network=udp4", `qname="example.com."`, "qtype=AAAA", "phase=response_validation", "latency=", "mismatched response"} {
		if !strings.Contains(validation.Error(), field) {
			t.Fatalf("validation diagnostic %q does not contain %q", validation, field)
		}
	}
	rcode := dnsProbeFailure("tcp4", "example.com.", dnsmessage.TypeA, "rcode", started, dnsRCodeName(dnsmessage.RCodeServerFailure), nil)
	for _, field := range []string{"network=tcp4", "qtype=A", "phase=rcode", "rcode=ServFail", "latency="} {
		if !strings.Contains(rcode.Error(), field) {
			t.Fatalf("RCode diagnostic %q does not contain %q", rcode, field)
		}
	}
}

func TestDNSProbeUsesEDNSAndRetriesTruncatedUDPThroughTCP(t *testing.T) {
	upstream := startDNSFixtureAt(t, DNSListenPort, true)
	probe := &DNSProbe{config: Config{GatewayAddress: netip.MustParseAddr("127.0.0.1")}}
	if err := probe.probe(context.Background(), "udp4", "truncated.example.", dnsmessage.TypeAAAA); err != nil {
		t.Fatal(err)
	}
	if !upstream.saw("udp4", "truncated.example.") || !upstream.saw("tcp4", "truncated.example.") || !upstream.edns() {
		t.Fatalf("truncated EDNS query did not traverse UDP then TCP: %#v", upstream.queries)
	}
}

func TestDNSProbeRunsMandatoryChecksSerially(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	var callsMu sync.Mutex
	var calls []string
	proxy := &DNSProbe{config: Config{ClusterDomain: "cluster.local"}}
	proxy.probeCheck = func(_ context.Context, network, name string, typeCode dnsmessage.Type) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		callsMu.Lock()
		calls = append(calls, fmt.Sprintf("%s/%s/%s", network, name, dnsTypeName(typeCode)))
		callsMu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return nil
	}

	if err := proxy.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"udp4/kubernetes.default.svc.cluster.local./A",
		"tcp4/kubernetes.default.svc.cluster.local./A",
		"udp4/example.com./A",
		"tcp4/example.com./A",
		"udp4/example.com./AAAA",
		"tcp4/example.com./AAAA",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("probe order = %#v, want %#v", calls, want)
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent readiness probes = %d, want 1", maximum.Load())
	}
}

func TestDNSProbeFailsClosedAtFirstFailedCheck(t *testing.T) {
	var calls int
	probeErr := errors.New("external UDP A unavailable")
	proxy := &DNSProbe{config: Config{ClusterDomain: "cluster.local"}}
	proxy.probeCheck = func(_ context.Context, _, _ string, _ dnsmessage.Type) error {
		calls++
		if calls == 3 {
			return probeErr
		}
		return nil
	}

	err := proxy.Probe(context.Background())
	if !errors.Is(err, probeErr) {
		t.Fatalf("probe error = %v, want %v", err, probeErr)
	}
	if calls != 3 {
		t.Fatalf("checks after first failure = %d, want 3 total", calls)
	}
}

func TestDNSProbeRetriesTransientLossWithinOriginalDeadline(t *testing.T) {
	upstream := startDNSFixture(t)
	upstream.setDropUDP(1)
	proxy := &DNSProbe{}
	request, identity, err := proxy.probeRequest("example.com.", dnsmessage.TypeAAAA)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, attempts, err := exchangeDNSProbe(context.Background(), "udp4", upstream.address.String(), request)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if elapsed := time.Since(started); elapsed < dnsProbeAttemptTimeout || elapsed >= dnsExchangeTimeout {
		t.Fatalf("transient-loss recovery took %s", elapsed)
	}
	if _, err := validateDNSResponse(response, identity); err != nil {
		t.Fatal(err)
	}
}

func TestDNSProbeFailsAfterBoundedAttempts(t *testing.T) {
	upstream := startDNSFixture(t)
	upstream.setDropUDP(dnsProbeMaximumAttempts)
	proxy := &DNSProbe{}
	request, _, err := proxy.probeRequest("example.com.", dnsmessage.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, attempts, err := exchangeDNSProbe(context.Background(), "udp4", upstream.address.String(), request)
	if err == nil {
		t.Fatal("sustained DNS loss was hidden")
	}
	if attempts != dnsProbeMaximumAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, dnsProbeMaximumAttempts)
	}
	if elapsed := time.Since(started); elapsed < dnsExchangeTimeout-100*time.Millisecond || elapsed > dnsExchangeTimeout+500*time.Millisecond {
		t.Fatalf("sustained-loss deadline = %s", elapsed)
	}
}

type dnsFixture struct {
	address     netip.AddrPort
	mu          sync.Mutex
	queries     map[string]int
	sawEDNS0    bool
	truncateUDP bool
	dropUDP     int
}

func startDNSFixture(t *testing.T) *dnsFixture {
	return startDNSFixtureAt(t, 0, false)
}

func startDNSFixtureAt(t *testing.T, requestedPort uint16, truncateUDP bool) *dnsFixture {
	t.Helper()
	udp, err := net.ListenPacket("udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(requestedPort))))
	if err != nil {
		t.Fatal(err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	tcp, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		_ = udp.Close()
		t.Fatal(err)
	}
	fixture := &dnsFixture{address: netip.MustParseAddrPort(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))), queries: map[string]int{}, truncateUDP: truncateUDP}
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
		fixture.mu.Lock()
		drop := fixture.dropUDP > 0
		if drop {
			fixture.dropUDP--
		}
		fixture.mu.Unlock()
		if drop {
			continue
		}
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
	if name == "servfail.example." {
		response.RCode = dnsmessage.RCodeServerFailure
		return response.Pack()
	}
	if network == "udp4" && fixture.truncateUDP {
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
	return response.Pack()
}

func (fixture *dnsFixture) saw(network, name string) bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.queries[network+"|"+name] > 0
}

func (fixture *dnsFixture) edns() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.sawEDNS0
}

func (fixture *dnsFixture) setDropUDP(count int) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.dropUDP = count
}
