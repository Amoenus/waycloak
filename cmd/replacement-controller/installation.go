// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"os"

	"github.com/Amoenus/waycloak/internal/installation"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func runInstallationController(args []string) error {
	flags := flag.NewFlagSet("installation-controller", flag.ContinueOnError)
	namespace := flags.String("namespace", os.Getenv("POD_NAMESPACE"), "namespace containing the installation journal and leader Lease")
	runtimeNamespace := flags.String("runtime-namespace", "waycloak-system", "owned runtime Helm release namespace")
	probeAddress := flags.String("health-probe-bind-address", ":8081", "health listener")
	options := zap.Options{}
	options.BindFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&options)))
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	return installation.Run(ctrl.SetupSignalHandler(), config, *namespace, *runtimeNamespace, *probeAddress)
}
