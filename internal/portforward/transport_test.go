// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRuntimeHandlerUsesExactVersionedLeaseIdentity(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	handler := RuntimeHandler{GatewayUID: "gateway-uid", Manager: &GatewayRuntimeManager{
		PortForward: &managerDriver{now: now}, Rules: &managerRules{}, Delivery: &managerDelivery{}, Now: func() time.Time { return now },
	}}
	intent := managerIntent("pod-a", 1)
	body, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, runtimePath(intent.LeaseUID, ""), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("runtime response = %d: %s", response.Code, response.Body.String())
	}
	var observation Observation
	if err := decodeStrict(response.Body.Bytes(), &observation); err != nil || !ExactObservation(observation, intent) || !observation.GatewayRulesReady {
		t.Fatalf("runtime observation = %#v, %v", observation, err)
	}

	request = httptest.NewRequest(http.MethodPut, runtimePath("different-uid", ""), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("path/body UID mismatch status = %d", response.Code)
	}
}

func TestRuntimeHandlerRejectsUnknownFieldsOversizeAndWrongMethod(t *testing.T) {
	handler := RuntimeHandler{GatewayUID: "gateway-uid", Manager: &GatewayRuntimeManager{}}
	for name, testCase := range map[string]struct {
		method string
		path   string
		body   string
		want   int
	}{
		"unknown-field": {http.MethodPut, runtimePath("lease-uid", ""), `{"apiVersion":"port-forward-runtime.waycloak.io/v1","leaseUID":"lease-uid","unknown":true}`, http.StatusBadRequest},
		"oversize":      {http.MethodPut, runtimePath("lease-uid", ""), strings.Repeat("x", int(runtimeBodyLimit)+1), http.StatusRequestEntityTooLarge},
		"wrong-method":  {http.MethodDelete, runtimePath("lease-uid", ""), `{}`, http.StatusMethodNotAllowed},
		"query":         {http.MethodPut, runtimePath("lease-uid", "") + "?debug=true", `{}`, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status = %d, want %d", response.Code, testCase.want)
			}
		})
	}
}

func TestRuntimeURLRejectsNonHTTPSAndAmbientURLFeatures(t *testing.T) {
	for _, endpoint := range []string{"http://gateway.test", "https://user@gateway.test", "https://gateway.test/path", "https://gateway.test?debug=true"} {
		if _, err := NewRuntimeClient(endpoint, "missing-ca", "missing-cert", "missing-key"); err == nil {
			t.Fatalf("unsafe runtime endpoint %q was accepted", endpoint)
		}
	}
}

func TestGatewayRuntimeServiceIdentityIsDeterministicAndNamespaceScoped(t *testing.T) {
	first := GatewayRuntimeServiceName("network-a", strings.Repeat("gateway", 30))
	if first != GatewayRuntimeServiceName("network-a", strings.Repeat("gateway", 30)) || first == GatewayRuntimeServiceName("network-b", strings.Repeat("gateway", 30)) || len(first) > 63 {
		t.Fatalf("gateway runtime Service identities are unsafe: %q", first)
	}
	endpoint := gatewayRuntimeEndpoint("network-a", "private", 9443)
	if endpoint.Scheme != "https" || endpoint.Host != GatewayRuntimeServiceName("network-a", "private")+".network-a.svc:9443" {
		t.Fatalf("gateway runtime endpoint = %s", endpoint)
	}
}

func TestGatewayRuntimeRequiresExactSPIFFEClientIdentity(t *testing.T) {
	verify, err := peerIdentityVerifier("spiffe://waycloak.io/replacement-controller")
	if err != nil {
		t.Fatal(err)
	}
	authorized, _ := url.Parse("spiffe://waycloak.io/replacement-controller")
	other, _ := url.Parse("spiffe://waycloak.io/other-controller")
	if err := verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{authorized}}}}); err != nil {
		t.Fatalf("authorized identity rejected: %v", err)
	}
	if err := verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{other}}}}); err == nil {
		t.Fatal("different identity from the trusted CA was accepted")
	}
	for _, invalid := range []string{"", "https://waycloak.io/controller", "spiffe:///missing-trust-domain", "spiffe://waycloak.io/controller?debug=true"} {
		if _, err := peerIdentityVerifier(invalid); err == nil {
			t.Fatalf("invalid controller identity %q was accepted", invalid)
		}
	}
}

func TestGatewayRuntimeMutualTLSAuthorizesOnlyExactControllerIdentity(t *testing.T) {
	directory := t.TempDir()
	caCertificate, caKey, caFile := writeTestCA(t, directory)
	serverCert, serverKey := writeTestCertificate(t, directory, "server", caCertificate, caKey, nil, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth)
	authorizedURI, _ := url.Parse("spiffe://waycloak.io/replacement-controller")
	otherURI, _ := url.Parse("spiffe://waycloak.io/other-controller")
	authorizedCert, authorizedKey := writeTestCertificate(t, directory, "authorized", caCertificate, caKey, []*url.URL{authorizedURI}, nil, x509.ExtKeyUsageClientAuth)
	otherCert, otherKey := writeTestCertificate(t, directory, "other", caCertificate, caKey, []*url.URL{otherURI}, nil, x509.ExtKeyUsageClientAuth)

	serverTLS, err := ServerTLSConfig(caFile, authorizedURI.String())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.LoadX509KeyPair(serverCert, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS.Certificates = []tls.Certificate{certificate}
	now := time.Unix(2000, 0).UTC()
	server := httptest.NewUnstartedServer(RuntimeHandler{GatewayUID: "gateway-uid", Manager: &GatewayRuntimeManager{
		PortForward: &managerDriver{now: now}, Rules: &managerRules{}, Delivery: &managerDelivery{}, Now: func() time.Time { return now },
	}})
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()

	client, err := NewRuntimeClient(server.URL, caFile, authorizedCert, authorizedKey)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{UID: "gateway-uid"}}
	intent := managerIntent("pod-a", 1)
	observation, err := client.Reconcile(context.Background(), gateway, intent)
	if err != nil || !ExactObservation(observation, intent) {
		t.Fatalf("authorized runtime observation = %#v, %v", observation, err)
	}

	unauthorized, err := NewRuntimeClient(server.URL, caFile, otherCert, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unauthorized.Reconcile(context.Background(), gateway, intent); err == nil {
		t.Fatal("client signed by the trusted CA with a different SPIFFE identity was authorized")
	}
}

func writeTestCA(t *testing.T, directory string) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Waycloak test CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key, path
}

func writeTestCertificate(t *testing.T, directory, name string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, identities []*url.URL, addresses []net.IP, usage x509.ExtKeyUsage) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{usage}, URIs: identities, IPAddresses: addresses}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(directory, name+".pem")
	keyPath := filepath.Join(directory, name+"-key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
