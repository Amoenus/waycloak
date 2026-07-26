// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package cni

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRootPeerOnlyHandlerRejectsMissingAndNonRootIdentity(t *testing.T) {
	dispatched := false
	handler := RootPeerOnlyHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dispatched = true }))
	for name, uid := range map[string]*uint32{"missing": nil, "non-root": pointerTo(uint32(1000))} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, statusPath, nil)
			if uid != nil {
				request = request.WithContext(context.WithValue(request.Context(), localPeerContextKey{}, *uid))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
	if dispatched {
		t.Fatal("unauthorized peer reached the operation handler")
	}

	request := httptest.NewRequest(http.MethodGet, statusPath, nil)
	request = request.WithContext(context.WithValue(request.Context(), localPeerContextKey{}, uint32(0)))
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !dispatched {
		t.Fatal("root peer did not reach the operation handler")
	}
}

func TestUnixPeerCredentialsMatchEffectiveUser(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()
	client, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	if server == nil {
		t.Fatal("server did not accept Unix connection")
	}
	defer server.Close()
	for name, connection := range map[string]net.Conn{"client": client, "server": server} {
		t.Run(name, func(t *testing.T) {
			uid, err := unixPeerUID(connection)
			if err != nil {
				t.Fatal(err)
			}
			if uid != uint32(os.Geteuid()) {
				t.Fatalf("peer UID = %d, effective UID = %d", uid, os.Geteuid())
			}
		})
	}
}

func pointerTo[T any](value T) *T { return &value }
