// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package cni

import (
	"errors"
	"os"
)

func validateProtocolKeyOwner(os.FileInfo) error {
	return errors.New("local protocol authentication is supported only on Linux")
}
