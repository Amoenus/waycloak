// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"os"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var metricsAddress, probeAddress string
	var leaderElection bool
	flag.StringVar(&metricsAddress, "metrics-bind-address", ":8080", "metrics listener")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8081", "health listener")
	flag.BoolVar(&leaderElection, "leader-elect", true, "use Kubernetes leader election")
	options := zap.Options{Development: false}
	options.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&options)))

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(wayv1.AddToScheme(scheme))
	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: metricsAddress}, HealthProbeBindAddress: probeAddress,
		LeaderElection: leaderElection, LeaderElectionID: "replacement-controller.networking.waycloak.io",
	})
	if err != nil {
		ctrl.Log.Error(err, "create replacement manager")
		os.Exit(1)
	}
	if err = (&waycontroller.VPNEgressRouteReconciler{Client: manager.GetClient(), Scheme: manager.GetScheme()}).SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "setup VPNEgressRoute controller")
		os.Exit(1)
	}
	if err = (&waycontroller.PodBindingReconciler{Client: manager.GetClient()}).SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "setup Pod binding controller")
		os.Exit(1)
	}
	if err = (&waycontroller.VPNWorkloadBindingReconciler{Client: manager.GetClient()}).SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "setup VPNWorkloadBinding controller")
		os.Exit(1)
	}
	if err = manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "add health check")
		os.Exit(1)
	}
	if err = manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "add ready check")
		os.Exit(1)
	}
	if err = manager.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "run replacement manager")
		os.Exit(1)
	}
}
