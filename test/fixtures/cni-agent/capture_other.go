// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build !linux

package main

import (
	"context"
	"errors"
)

func startPacketCapture(context.Context, string) error {
	return errors.New("packet capture is supported only on Linux")
}

func probeDirectEgress(string) {}
