// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package cni

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// LocalPeerContext leaves context unchanged on unsupported platforms.
func LocalPeerContext(ctx context.Context, _ net.Conn) context.Context { return ctx }

// RootPeerOnlyHandler rejects the protocol on unsupported platforms.
func RootPeerOnlyHandler(http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "local protocol is supported only on Linux", http.StatusNotImplemented)
	})
}

// VerifyRootAgentPeer rejects the protocol on unsupported platforms.
func VerifyRootAgentPeer(net.Conn) error {
	return errors.New("local protocol peer authentication is supported only on Linux")
}
