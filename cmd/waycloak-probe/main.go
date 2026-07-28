// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Amoenus/waycloak/internal/verifyprobe"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := verifyprobe.Run(ctx, os.Getenv); err != nil {
		log.Fatal(err)
	}
}
