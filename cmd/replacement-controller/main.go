// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"os"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"github.com/Amoenus/waycloak/internal/observationrelay"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var metricsAddress, probeAddress, observationAddress, observationCert, observationKey, observationAgentNamespace, observationAgentServiceAccount string
	var leaderElection bool
	flag.StringVar(&metricsAddress, "metrics-bind-address", ":8080", "metrics listener")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8081", "health listener")
	flag.BoolVar(&leaderElection, "leader-elect", true, "use Kubernetes leader election")
	flag.StringVar(&observationAddress, "observation-bind-address", "", "HTTPS node-observation listener; empty disables it")
	flag.StringVar(&observationCert, "observation-tls-cert", "", "node-observation TLS certificate")
	flag.StringVar(&observationKey, "observation-tls-key", "", "node-observation TLS private key")
	flag.StringVar(&observationAgentNamespace, "observation-agent-namespace", "", "authorized node-agent namespace")
	flag.StringVar(&observationAgentServiceAccount, "observation-agent-service-account", "", "authorized node-agent service account")
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
	ctx := ctrl.SetupSignalHandler()
	if observationAddress != "" {
		if observationCert == "" || observationKey == "" || observationAgentNamespace == "" || observationAgentServiceAccount == "" {
			ctrl.Log.Error(nil, "observation TLS identity and exact node-agent service account are required")
			os.Exit(1)
		}
		clientset, clientErr := kubernetes.NewForConfig(manager.GetConfig())
		if clientErr != nil {
			ctrl.Log.Error(clientErr, "create observation TokenReview client")
			os.Exit(1)
		}
		relay := (&observationrelay.Relay{
			Reviewer: clientset.AuthenticationV1().TokenReviews(), Reader: manager.GetAPIReader(), Writer: manager.GetClient(),
			AgentNamespace: observationAgentNamespace, AgentServiceAccount: observationAgentServiceAccount,
		}).Handler()
		server := &http.Server{Addr: observationAddress, Handler: relay, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
		go serveObservations(ctx, server, observationCert, observationKey)
	}
	if err = manager.Start(ctx); err != nil {
		ctrl.Log.Error(err, "run replacement manager")
		os.Exit(1)
	}
}

func serveObservations(ctx context.Context, server *http.Server, certFile, keyFile string) {
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
		ctrl.Log.Error(err, "serve authenticated node observations")
		os.Exit(1)
	}
}
