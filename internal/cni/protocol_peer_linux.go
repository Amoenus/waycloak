// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package cni

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"golang.org/x/sys/unix"
)

type localPeerContextKey struct{}

func LocalPeerContext(ctx context.Context, connection net.Conn) context.Context {
	uid, err := unixPeerUID(connection)
	if err != nil {
		return context.WithValue(ctx, localPeerContextKey{}, ^uint32(0))
	}
	return context.WithValue(ctx, localPeerContextKey{}, uid)
}

func RootPeerOnlyHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		uid, ok := request.Context().Value(localPeerContextKey{}).(uint32)
		if !ok || uid != 0 {
			http.Error(response, "local protocol request rejected", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func VerifyRootAgentPeer(connection net.Conn) error {
	uid, err := unixPeerUID(connection)
	if err != nil {
		return err
	}
	if uid != 0 {
		return fmt.Errorf("local agent peer UID is %d, expected root", uid)
	}
	return nil
}

func unixPeerUID(connection net.Conn) (uint32, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, errors.New("local protocol connection is not a Unix socket")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credential *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if socketErr != nil {
		return 0, socketErr
	}
	if credential == nil {
		return 0, errors.New("local protocol peer credentials are unavailable")
	}
	return credential.Uid, nil
}
