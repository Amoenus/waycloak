// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Package httpserverlog provides narrowly filtered logging for Waycloak HTTP
// servers whose TLS listeners are also used by Kubernetes TCP socket probes.
package httpserverlog

import (
	"io"
	"log"
	"strings"
)

const tlsHandshakeErrorPrefix = "http: TLS handshake error from "

// NewTLSProbeErrorLogger returns an HTTP server error logger that drops only
// the benign EOF produced when a Kubernetes TCP socket probe connects to a TLS
// listener and immediately closes it. Other TLS and HTTP server errors remain
// visible.
func NewTLSProbeErrorLogger(output io.Writer) *log.Logger {
	return log.New(tlsProbeErrorWriter{output: output}, "", log.LstdFlags)
}

type tlsProbeErrorWriter struct {
	output io.Writer
}

func (writer tlsProbeErrorWriter) Write(message []byte) (int, error) {
	line := strings.TrimSpace(string(message))
	if strings.Contains(line, tlsHandshakeErrorPrefix) && strings.HasSuffix(line, ": EOF") {
		return len(message), nil
	}
	return writer.output.Write(message)
}
