// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"context"
	"errors"
	"time"

	installv1 "github.com/Amoenus/waycloak/api/installation/v1alpha1"
	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"helm.sh/helm/v4/pkg/action"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Run starts a separate elected installation process. The networking controller
// continues to own the data-plane API; neither process executes a CLI.
func Run(ctx context.Context, config *rest.Config, namespace, runtimeNamespace, probeAddress string) error {
	if namespace == "" || runtimeNamespace == "" {
		return errors.New("installation and runtime namespaces are required")
	}
	scheme := runtime.NewScheme()
	if err := installv1.AddToScheme(scheme); err != nil {
		return err
	}
	leaseDuration, renewDeadline, retryPeriod := 45*time.Second, 30*time.Second, 5*time.Second
	manager, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: probeAddress,
		LeaderElection: true, LeaderElectionID: "waycloak-installation-controller", LeaderElectionNamespace: namespace,
		LeaseDuration: &leaseDuration, RenewDeadline: &renewDeadline, RetryPeriod: &retryPeriod})
	if err != nil {
		return err
	}
	clients, err := waycloakctl.NewClientsForConfig(config)
	if err != nil {
		return err
	}
	actions := new(action.Configuration)
	if err := actions.Init(&restGetter{config: config, namespace: runtimeNamespace}, runtimeNamespace, "secret"); err != nil {
		return err
	}
	backend := &Backend{Clients: clients, Resolver: &ReleaseResolver{}, Runtime: &HelmRuntime{Configuration: actions, Namespace: runtimeNamespace}}
	reconciler := &Reconciler{Client: manager.GetClient(), Clients: clients, Namespace: namespace, Backend: backend}
	if err := reconciler.SetupWithManager(manager); err != nil {
		return err
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return manager.Start(ctx)
}

type restGetter struct {
	config    *rest.Config
	namespace string
}

func (g *restGetter) ToRESTConfig() (*rest.Config, error) { return rest.CopyConfig(g.config), nil }
func (g *restGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	client, err := discovery.NewDiscoveryClientForConfig(g.config)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(client), nil
}
func (g *restGetter) ToRESTMapper() (meta.RESTMapper, error) {
	discovery, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(discovery), nil
}
func (g *restGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewDefaultClientConfig(clientcmdapi.Config{}, &clientcmd.ConfigOverrides{Context: clientcmdapi.Context{Namespace: g.namespace}})
}
