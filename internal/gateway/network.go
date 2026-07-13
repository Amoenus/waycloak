// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
)

var ErrNetworkUnsupported = errors.New("gateway networking is unsupported on this platform")

type Network interface {
	Reconcile(context.Context, DesiredState) error
}

func OverlayInterfaceName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return fmt.Sprintf("wcg%x", sum[:5])
}
