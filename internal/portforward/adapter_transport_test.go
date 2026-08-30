// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	"golang.org/x/net/dns/dnsmessage"
)

func TestHTTPAdapterClientClassifiesConflictAndUnavailable(t *testing.T) {
	for _, test := range []struct {
		status int
		kind   AdapterFailureKind
	}{{http.StatusConflict, AdapterFailureConflict}, {http.StatusServiceUnavailable, AdapterFailureUnavailable}} {
		t.Run(test.kind.String(), func(t *testing.T) {
			intent := managerIntent("pod-a", 4)
			intent.AdapterName = "qbittorrent"
			record := AdapterLeaseRecord{APIVersion: AdapterAPIVersion, LeaseNamespace: intent.LeaseNamespace, LeaseUID: intent.LeaseUID,
				HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID}
			client := &HTTPAdapterClient{Port: 9443, ClusterDomain: "cluster.local", Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(`{"error":"request rejected"}`))}, nil
			})}}
			_, err := client.Deliver(context.Background(), intent.AdapterName, record)
			var requestError *AdapterRequestError
			if !errors.As(err, &requestError) || requestError.Kind != test.kind {
				t.Fatalf("classified error = %#v, %v", requestError, err)
			}
		})
	}
}

func TestHTTPAdapterClientUsesDeterministicCredentialFreeExactEndpoint(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	intent := managerIntent("pod-a", 4)
	intent.AdapterName = "qbittorrent"
	record := AdapterLeaseRecord{APIVersion: AdapterAPIVersion, LeaseNamespace: intent.LeaseNamespace, LeaseUID: intent.LeaseUID,
		HandoffGeneration: intent.HandoffGeneration, PodUID: intent.PodUID, ExpiresAt: now.Add(time.Minute)}
	requests := 0
	client := &HTTPAdapterClient{Port: 9443, ClusterDomain: "cluster.local", Now: func() time.Time { return time.Now().UTC() }, Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Scheme != "https" || request.URL.Host != AdapterServiceName(intent.LeaseNamespace, intent.AdapterName)+".apps.svc.cluster.local:9443" ||
			request.URL.Path != adapterPath(intent.LeaseUID, "") || request.Header.Get("Authorization") != "" {
			t.Fatalf("adapter request = %s headers=%v", request.URL, request.Header)
		}
		var got AdapterLeaseRecord
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil || got.LeaseUID != record.LeaseUID || got.PodUID != record.PodUID {
			t.Fatalf("adapter record = %#v, %v", got, err)
		}
		ack := AdapterAcknowledgement{APIVersion: AdapterAPIVersion, LeaseNamespace: record.LeaseNamespace, LeaseUID: record.LeaseUID,
			HandoffGeneration: record.HandoffGeneration, PodUID: record.PodUID, ObservedAt: now, ExpiresAt: record.ExpiresAt}
		return adapterJSONResponse(t, ack), nil
	})}}
	acknowledgement, err := client.Deliver(context.Background(), intent.AdapterName, record)
	if err != nil || acknowledgement.PodUID != intent.PodUID || requests != 1 {
		t.Fatalf("adapter acknowledgement = %#v, calls=%d, %v", acknowledgement, requests, err)
	}
}

func TestHTTPAdapterClientRejectsMismatchedWithdrawalAcknowledgement(t *testing.T) {
	intent := AdapterWithdrawalIntent{APIVersion: AdapterAPIVersion, LeaseNamespace: "apps", LeaseUID: "lease-uid", HandoffGeneration: 2, PodUID: "pod-a"}
	client := &HTTPAdapterClient{Port: 9443, ClusterDomain: "cluster.local", Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != adapterPath(intent.LeaseUID, "withdraw") {
			t.Fatalf("withdrawal path = %s", request.URL.Path)
		}
		return adapterJSONResponse(t, AdapterWithdrawalAcknowledgement{APIVersion: AdapterAPIVersion, LeaseNamespace: intent.LeaseNamespace,
			LeaseUID: intent.LeaseUID, HandoffGeneration: intent.HandoffGeneration, PodUID: "different-pod", ObservedAt: time.Now(), Withdrawn: true}), nil
	})}}
	if withdrawn, err := client.Withdraw(context.Background(), "qbittorrent", intent); err != nil || withdrawn {
		t.Fatalf("mismatched withdrawal = %t, %v", withdrawn, err)
	}
}

func TestHTTPAdapterClientObservesExactHealthyAdapterIdentity(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	namespace := wayv1.NamespaceName("apps")
	name := wayv1.ObjectName("qbittorrent")
	image := "registry.invalid/adapter@sha256:" + strings.Repeat("a", 64)
	client := &HTTPAdapterClient{Port: DefaultAdapterPort, ClusterDomain: "cluster.local", Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/networking.waycloak.io/adapter/v1/healthz" ||
			request.URL.Host != AdapterServiceName(namespace, name)+".apps.svc.cluster.local:9444" || request.Header.Get("Authorization") != "" {
			t.Fatalf("adapter health request = %s %s headers=%v", request.Method, request.URL, request.Header)
		}
		return adapterJSONResponse(t, AdapterHealthObservation{APIVersion: AdapterAPIVersion, Namespace: namespace, Name: name,
			Image: image, PodUID: "adapter-pod-uid", ObservedAt: now, Ready: true}), nil
	})}}
	observation, err := client.Observe(context.Background(), namespace, name, image)
	if err != nil || !observation.Ready || observation.PodUID != "adapter-pod-uid" {
		t.Fatalf("adapter health observation = %#v, %v", observation, err)
	}

	client.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return adapterJSONResponse(t, AdapterHealthObservation{APIVersion: AdapterAPIVersion, Namespace: namespace, Name: "other",
			Image: image, PodUID: "adapter-pod-uid", ObservedAt: now, Ready: true}), nil
	})
	if _, err := client.Observe(context.Background(), namespace, name, image); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("mismatched adapter health error = %v", err)
	}
}

func TestAdapterDialerUsesExplicitResolverAndRecoversAfterFailedLookup(t *testing.T) {
	dns, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dns.Close()
	dnsUDPAddress := dns.LocalAddr().(*net.UDPAddr)
	dnsTCP, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: dnsUDPAddress.IP, Port: dnsUDPAddress.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer dnsTCP.Close()
	var queries atomic.Int32
	var resolverReady atomic.Bool
	go func() {
		buffer := make([]byte, 1232)
		for {
			length, peer, readErr := dns.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			var parser dnsmessage.Parser
			header, parseErr := parser.Start(buffer[:length])
			if parseErr != nil {
				continue
			}
			question, parseErr := parser.Question()
			if parseErr != nil {
				continue
			}
			responseHeader := dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true}
			queries.Add(1)
			if !resolverReady.Load() {
				responseHeader.RCode = dnsmessage.RCodeNameError
			}
			builder := dnsmessage.NewBuilder(nil, responseHeader)
			builder.EnableCompression()
			if builder.StartQuestions() != nil || builder.Question(question) != nil || builder.StartAnswers() != nil {
				continue
			}
			if responseHeader.RCode == dnsmessage.RCodeSuccess && question.Type == dnsmessage.TypeA {
				resourceHeader := dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}
				if builder.AResource(resourceHeader, dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}) != nil {
					continue
				}
			}
			response, buildErr := builder.Finish()
			if buildErr == nil {
				_, _ = dns.WriteTo(response, peer)
			}
		}
	}()

	resolverEndpoint := netip.MustParseAddrPort(dns.LocalAddr().String())
	dialer := newAdapterDialer(resolverEndpoint)
	if _, err := dialer.Resolver.LookupNetIP(context.Background(), "ip4", "adapter.apps.svc.cluster.local"); err == nil {
		t.Fatal("first failed sidecar lookup unexpectedly succeeded")
	}
	failedQueries := queries.Load()
	resolverReady.Store(true)
	addresses, err := dialer.Resolver.LookupNetIP(context.Background(), "ip4", "adapter.apps.svc.cluster.local")
	if err != nil || len(addresses) != 1 || addresses[0].String() != "127.0.0.1" || failedQueries < 1 || queries.Load() <= failedQueries {
		t.Fatalf("sidecar lookup recovery = %v queries=%d, %v", addresses, queries.Load(), err)
	}
	tcpResolverConnection, err := dialer.Resolver.Dial(context.Background(), "tcp", "192.0.2.53:53")
	if err != nil {
		t.Fatalf("TCP resolver path did not use the explicit sidecar: %v", err)
	}
	acceptedDNSConnection, err := dnsTCP.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = acceptedDNSConnection.Close()
	_ = tcpResolverConnection.Close()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	connection, err := dialer.DialContext(context.Background(), "tcp4", net.JoinHostPort("adapter.apps.svc.cluster.local", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("adapter transport did not use the explicit resolver: %v", err)
	}
	_ = connection.Close()
}

func TestHTTPAdapterClientRequiresExactIPResolver(t *testing.T) {
	for _, resolverAddress := range []string{"", "127.0.0.1", "127.0.0.1:0", "dns.cluster.local:1053"} {
		_, err := NewHTTPAdapterClientWithResolver("missing-ca", "missing-cert", "missing-key", DefaultAdapterPort, "cluster.local", resolverAddress)
		if err == nil || err.Error() != "exact adapter DNS resolver address is required" {
			t.Fatalf("resolver %q validation error = %v", resolverAddress, err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func adapterJSONResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
