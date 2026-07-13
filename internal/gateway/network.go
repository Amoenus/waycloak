// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
)

var ErrNetworkUnsupported = errors.New("gateway networking is unsupported on this platform")

type Network interface {
	Reconcile(context.Context, DesiredState) error
}
