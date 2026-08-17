// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package httpserverlog

import (
	"bytes"
	"strings"
	"testing"
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
