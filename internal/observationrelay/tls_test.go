// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package observationrelay

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileCertificateReloadsAndFailsClosed(t *testing.T) {
	directory := t.TempDir()
	certFile := filepath.Join(directory, "tls.crt")
	keyFile := filepath.Join(directory, "tls.key")
	firstCert, firstKey := testServingIdentity(t, 1)
	secondCert, secondKey := testServingIdentity(t, 2)
	writeIdentityFile(t, certFile, firstCert)
	writeIdentityFile(t, keyFile, firstKey)

	loader := FileCertificate{CertFile: certFile, KeyFile: keyFile}
	first, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("load first identity: %v", err)
	}

	writeIdentityFile(t, certFile, secondCert)
	if _, err := loader.GetCertificate(nil); err == nil {
		t.Fatal("mismatched projected certificate and key must fail closed")
	}

	writeIdentityFile(t, keyFile, secondKey)
	second, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("load rotated identity: %v", err)
	}
	if string(first.Certificate[0]) == string(second.Certificate[0]) {
		t.Fatal("new handshake reused the prior serving certificate")
	}

	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.GetCertificate(nil); err == nil {
		t.Fatal("missing projected private key must fail closed")
	}
}

func testServingIdentity(t *testing.T, serial int64) ([]byte, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "observation.test"},
		DNSNames:     []string{"observation.test"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
}

func writeIdentityFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
