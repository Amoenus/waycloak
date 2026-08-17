// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package httpserverlog

import (
	"bytes"
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTLSProbeErrorLogger(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "tcp probe eof is suppressed",
			message: "http: TLS handshake error from 192.0.2.1:12345: EOF",
		},
		{
			name:    "certificate failure remains visible",
			message: "http: TLS handshake error from 192.0.2.1:12345: remote error: tls: bad certificate",
			want:    "remote error: tls: bad certificate",
		},
		{
			name:    "unrelated eof remains visible",
			message: "http: Accept error: EOF",
			want:    "http: Accept error: EOF",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := NewTLSProbeErrorLogger(&output)
			logger.Print(test.message)
			if test.want == "" {
				if output.Len() != 0 {
					t.Fatalf("expected no output, got %q", output.String())
				}
				return
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("expected output to contain %q, got %q", test.want, output.String())
			}
		})
	}
}

func TestTLSProbeErrorLoggerWithRealMTLSListeners(t *testing.T) {
	for _, listener := range []string{"gateway runtime", "qBittorrent adapter"} {
		t.Run(listener, func(t *testing.T) {
			var baseline lockedBuffer
			baselineServer := newMTLSTestServer(t, log.New(&baseline, "", 0))
			openAndCloseTCP(t, baselineServer.Listener.Addr().String())
			waitForLog(t, &baseline, ": EOF")
			baselineServer.Close()

			var filtered lockedBuffer
			server := newMTLSTestServer(t, NewTLSProbeErrorLogger(&filtered))
			defer server.Close()
			openAndCloseTCP(t, server.Listener.Addr().String())

			transport, ok := server.Client().Transport.(*http.Transport)
			if !ok {
				t.Fatalf("unexpected test client transport %T", server.Client().Transport)
			}
			clientTLS := transport.TLSClientConfig.Clone()
			clientTLS.Certificates = nil
			connection, err := tls.Dial("tcp", server.Listener.Addr().String(), clientTLS)
			if err == nil {
				_, writeErr := connection.Write([]byte("GET / HTTP/1.1\r\nHost: test\r\n\r\n"))
				var response [1]byte
				_, readErr := connection.Read(response[:])
				_ = connection.Close()
				if writeErr == nil && readErr == nil {
					t.Fatal("expected mTLS request without a client certificate to fail")
				}
			}
			waitForLog(t, &filtered, "certificate")

			// Allow the earlier raw TCP connection to reach the TLS accept loop.
			time.Sleep(50 * time.Millisecond)
			output := filtered.String()
			if strings.Contains(output, ": EOF") {
				t.Fatalf("expected TCP probe EOF to be suppressed, got %q", output)
			}
			if !strings.Contains(output, "TLS handshake error") {
				t.Fatalf("expected certificate failure to remain an HTTP server error, got %q", output)
			}
		})
	}
}

func newMTLSTestServer(t *testing.T, errorLogger *log.Logger) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Config.ErrorLog = errorLogger
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	return server
}

func openAndCloseTCP(t *testing.T, address string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("open TCP probe connection: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close TCP probe connection: %v", err)
	}
}

func waitForLog(t *testing.T, output *lockedBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for log containing %q; got %q", text, output.String())
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(message []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(message)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
