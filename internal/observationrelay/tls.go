// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package observationrelay

import (
	"crypto/tls"
	"fmt"
	"os"
)

// FileCertificate reloads and validates the projected serving identity for
// every TLS handshake. Kubernetes may update the two projected files in
// separate filesystem operations; a mismatched projection therefore fails the
// new handshake closed instead of retaining an unreviewed or stale identity.
type FileCertificate struct {
	CertFile string
	KeyFile  string
}

func (certificate FileCertificate) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	certPEM, err := os.ReadFile(certificate.CertFile)
	if err != nil {
		return nil, fmt.Errorf("read observation serving certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(certificate.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("read observation serving private key: %w", err)
	}
	identity, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load observation serving identity: %w", err)
	}
	return &identity, nil
}
