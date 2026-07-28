// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	runtimePathPrefix = "/port-forward-runtime.waycloak.io/v1/leases/"
	runtimeBodyLimit  = int64(64 << 10)
)

// RuntimeClient is a narrow controller-to-gateway client. Its transport never
// uses environment proxy settings and sends no Kubernetes object or credential.
type RuntimeClient struct {
	BaseURL *url.URL
	Client  *http.Client
}

// RuntimeRouter derives one immutable Service DNS identity per VPNGateway.
// Gateway UID remains part of every request and is checked by the serving
// runtime, so Service/name reuse cannot authorize a replacement UID.
type RuntimeRouter struct {
	Client *http.Client
	Port   uint16
}

func NewRuntimeRouter(caFile, certFile, keyFile string, port uint16) (*RuntimeRouter, error) {
	if port == 0 {
		return nil, errors.New("gateway runtime HTTPS port is required")
	}
	tlsConfig, err := clientTLSConfig(caFile, certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &RuntimeRouter{Port: port, Client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: nil, TLSClientConfig: tlsConfig, DisableCompression: true, MaxIdleConns: 16, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
	}}}, nil
}

func (r *RuntimeRouter) Reconcile(ctx context.Context, gateway *wayv1.VPNGateway, intent Intent) (Observation, error) {
	client, err := r.clientFor(gateway)
	if err != nil {
		return Observation{}, err
	}
	return client.Reconcile(ctx, gateway, intent)
}

func (r *RuntimeRouter) Withdraw(ctx context.Context, gateway *wayv1.VPNGateway, intent WithdrawalIntent) (Observation, error) {
	client, err := r.clientFor(gateway)
	if err != nil {
		return Observation{}, err
	}
	return client.Withdraw(ctx, gateway, intent)
}

func (*RuntimeRouter) Quarantine(context.Context, *wayv1.VPNGateway, WithdrawalIntent, time.Time) error {
	return nil
}

func (r *RuntimeRouter) clientFor(gateway *wayv1.VPNGateway) (*RuntimeClient, error) {
	if r == nil || r.Client == nil || r.Port == 0 || gateway == nil || gateway.Namespace == "" || gateway.Name == "" || gateway.UID == "" {
		return nil, errors.New("exact gateway runtime Service identity is required")
	}
	return &RuntimeClient{BaseURL: gatewayRuntimeEndpoint(gateway.Namespace, gateway.Name, r.Port), Client: r.Client}, nil
}

func NewRuntimeClient(rawURL, caFile, certFile, keyFile string) (*RuntimeClient, error) {
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, errors.New("gateway runtime URL must be an origin-only HTTPS URL")
	}
	tlsConfig, err := clientTLSConfig(caFile, certFile, keyFile)
	if err != nil {
		return nil, err
	}
	endpoint.Path = ""
	return &RuntimeClient{BaseURL: endpoint, Client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: nil, TLSClientConfig: tlsConfig, DisableCompression: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
	}}}, nil
}

func (c *RuntimeClient) Reconcile(ctx context.Context, gateway *wayv1.VPNGateway, intent Intent) (Observation, error) {
	if gateway == nil || intent.GatewayUID != wayv1.ObjectUID(gateway.UID) {
		return Observation{}, errors.New("exact gateway identity is required")
	}
	return c.call(ctx, http.MethodPut, runtimePath(intent.LeaseUID, ""), intent)
}

func (c *RuntimeClient) Withdraw(ctx context.Context, gateway *wayv1.VPNGateway, intent WithdrawalIntent) (Observation, error) {
	if gateway == nil || intent.GatewayUID != wayv1.ObjectUID(gateway.UID) {
		return Observation{}, errors.New("exact gateway identity is required")
	}
	return c.call(ctx, http.MethodPost, runtimePath(intent.LeaseUID, "withdraw"), intent)
}

func (*RuntimeClient) Quarantine(context.Context, *wayv1.VPNGateway, WithdrawalIntent, time.Time) error {
	// Numeric reuse quarantine is durable Kubernetes allocator state. It is not
	// delegated to the credential-free gateway runtime.
	return nil
}

func (c *RuntimeClient) call(ctx context.Context, method, path string, input any) (Observation, error) {
	if c == nil || c.BaseURL == nil || c.Client == nil {
		return Observation{}, errors.New("gateway runtime client is not configured")
	}
	body, err := json.Marshal(input)
	if err != nil || int64(len(body)) > runtimeBodyLimit {
		return Observation{}, errors.New("gateway runtime request is invalid")
	}
	requestURL := *c.BaseURL
	requestURL.Path = path
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return Observation{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.Client.Do(request)
	if err != nil {
		return Observation{}, fmt.Errorf("call gateway runtime: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := readBounded(response.Body, runtimeBodyLimit)
	if err != nil {
		return Observation{}, err
	}
	if response.StatusCode != http.StatusOK {
		return Observation{}, fmt.Errorf("gateway runtime rejected request with status %d", response.StatusCode)
	}
	var observation Observation
	if err := decodeStrict(responseBody, &observation); err != nil {
		return Observation{}, fmt.Errorf("decode gateway runtime observation: %w", err)
	}
	return observation, nil
}

// RuntimeHandler exposes no discovery, list, debug, or Kubernetes operation.
// The serving process is configured for one exact gateway UID.
type RuntimeHandler struct {
	Manager    *GatewayRuntimeManager
	GatewayUID wayv1.ObjectUID
}

func (h RuntimeHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	if h.Manager == nil || h.GatewayUID == "" || request.Header.Get("Content-Type") != "application/json" {
		writeRuntimeError(response, http.StatusBadRequest)
		return
	}
	leaseUID, operation, ok := parseRuntimePath(request.URL)
	if !ok {
		writeRuntimeError(response, http.StatusNotFound)
		return
	}
	body, err := readBounded(request.Body, runtimeBodyLimit)
	if err != nil {
		writeRuntimeError(response, http.StatusRequestEntityTooLarge)
		return
	}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{UID: typesUID(h.GatewayUID)}}
	var observation Observation
	switch {
	case operation == "" && request.Method == http.MethodPut:
		var intent Intent
		if decodeStrict(body, &intent) != nil || intent.LeaseUID != leaseUID || intent.GatewayUID != h.GatewayUID {
			writeRuntimeError(response, http.StatusBadRequest)
			return
		}
		observation, err = h.Manager.Reconcile(request.Context(), gateway, intent)
	case operation == "withdraw" && request.Method == http.MethodPost:
		var intent WithdrawalIntent
		if decodeStrict(body, &intent) != nil || intent.LeaseUID != leaseUID || intent.GatewayUID != h.GatewayUID {
			writeRuntimeError(response, http.StatusBadRequest)
			return
		}
		observation, err = h.Manager.Withdraw(request.Context(), gateway, intent)
	default:
		response.Header().Set("Allow", allowedMethod(operation))
		writeRuntimeError(response, http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		writeRuntimeError(response, http.StatusConflict)
		return
	}
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(observation)
}

func ServerTLSConfig(caFile, expectedIdentity string) (*tls.Config, error) {
	return ServerTLSConfigForIdentities(caFile, expectedIdentity)
}

func ServerTLSConfigForIdentities(caFile string, expectedIdentities ...string) (*tls.Config, error) {
	roots, err := certificatePool(caFile)
	if err != nil {
		return nil, err
	}
	verifyIdentity, err := peerIdentityVerifier(expectedIdentities...)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, VerifyConnection: verifyIdentity}, nil
}

func peerIdentityVerifier(expectedIdentities ...string) (func(tls.ConnectionState) error, error) {
	if len(expectedIdentities) == 0 {
		return nil, errors.New("at least one exact SPIFFE peer identity is required")
	}
	expected := make(map[string]struct{}, len(expectedIdentities))
	for _, value := range expectedIdentities {
		identity, err := url.Parse(value)
		if err != nil || identity.Scheme != "spiffe" || identity.Host == "" || identity.User != nil || identity.RawQuery != "" || identity.Fragment != "" {
			return nil, errors.New("exact SPIFFE peer identity is required")
		}
		expected[identity.String()] = struct{}{}
	}
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("gateway runtime client certificate is missing")
		}
		for _, identity := range state.PeerCertificates[0].URIs {
			if _, ok := expected[identity.String()]; ok {
				return nil
			}
		}
		return errors.New("gateway runtime client identity is unauthorized")
	}, nil
}

func PeerHasIdentity(request *http.Request, expectedIdentity string) bool {
	if request == nil || request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return false
	}
	for _, identity := range request.TLS.PeerCertificates[0].URIs {
		if identity.String() == expectedIdentity {
			return true
		}
	}
	return false
}

func clientTLSConfig(caFile, certFile, keyFile string) (*tls.Config, error) {
	if caFile == "" || certFile == "" || keyFile == "" {
		return nil, errors.New("gateway runtime CA and client certificate identity are required")
	}
	roots, err := certificatePool(caFile)
	if err != nil {
		return nil, err
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load gateway runtime client identity: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}}, nil
}

func certificatePool(path string) (*x509.CertPool, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read gateway runtime CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(contents) {
		return nil, errors.New("gateway runtime CA bundle contains no certificate")
	}
	return pool, nil
}

func parseRuntimePath(value *url.URL) (wayv1.ObjectUID, string, bool) {
	if value == nil || value.RawQuery != "" || !strings.HasPrefix(value.EscapedPath(), runtimePathPrefix) {
		return "", "", false
	}
	remainder := strings.TrimPrefix(value.EscapedPath(), runtimePathPrefix)
	parts := strings.Split(remainder, "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		return "", "", false
	}
	leaseUID, err := url.PathUnescape(parts[0])
	if err != nil || leaseUID == "" || strings.Contains(leaseUID, "/") || len(leaseUID) > 128 {
		return "", "", false
	}
	operation := ""
	if len(parts) == 2 {
		operation = parts[1]
		if operation != "withdraw" {
			return "", "", false
		}
	}
	return wayv1.ObjectUID(leaseUID), operation, true
}

func runtimePath(uid wayv1.ObjectUID, operation string) string {
	path := runtimePathPrefix + url.PathEscape(string(uid))
	if operation != "" {
		path += "/" + operation
	}
	return path
}

func GatewayRuntimeServiceName(namespace, name string) string {
	digest := sha256.Sum256([]byte(namespace + "/" + name))
	return fmt.Sprintf("waycloak-gateway-%x", digest[:8])
}

func gatewayRuntimeEndpoint(namespace, name string, port uint16) *url.URL {
	return &url.URL{Scheme: "https", Host: GatewayRuntimeServiceName(namespace, name) + "." + namespace + ".svc:" + strconv.Itoa(int(port))}
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, errors.New("gateway runtime message exceeds size limit")
	}
	return contents, nil
}

func decodeStrict(contents []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("gateway runtime message contains trailing data")
	}
	return nil
}

func writeRuntimeError(response http.ResponseWriter, status int) {
	response.WriteHeader(status)
	_, _ = response.Write([]byte(`{"apiVersion":"port-forward-runtime.waycloak.io/v1","error":"request rejected"}`))
}

func allowedMethod(operation string) string {
	if operation == "withdraw" {
		return http.MethodPost
	}
	return http.MethodPut
}

func typesUID(uid wayv1.ObjectUID) types.UID { return types.UID(uid) }

var _ Runtime = (*RuntimeClient)(nil)
var _ Runtime = (*RuntimeRouter)(nil)
var _ http.Handler = RuntimeHandler{}
