// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"net/netip"
	"os"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	"github.com/Amoenus/waycloak/internal/gatewayruntime"
	"github.com/Amoenus/waycloak/internal/observationrelay"
	"github.com/Amoenus/waycloak/internal/portforward"
	"github.com/Amoenus/waycloak/internal/scheduling"
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
	var gatewayControllerName, releaseVersion, releaseManifestDigest, conformanceProfile string
	var portForwardRuntimeCA, portForwardRuntimeCert, portForwardRuntimeKey string
	var portForwardRuntimePort uint
	var adapterCA, adapterCert, adapterKey string
	var adapterPort uint
	var gatewayEngineImage, gatewayAgentImage, gatewayOverlayCIDR string
	var gatewayVNI, gatewayVXLANPort, gatewayHealthPort uint
	var gatewayMTU int
	var leaderElection bool
	flag.StringVar(&metricsAddress, "metrics-bind-address", ":8080", "metrics listener")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8081", "health listener")
	flag.BoolVar(&leaderElection, "leader-elect", true, "use Kubernetes leader election")
	flag.StringVar(&observationAddress, "observation-bind-address", "", "HTTPS node-observation listener; empty disables it")
	flag.StringVar(&observationCert, "observation-tls-cert", "", "node-observation TLS certificate")
	flag.StringVar(&observationKey, "observation-tls-key", "", "node-observation TLS private key")
	flag.StringVar(&observationAgentNamespace, "observation-agent-namespace", "", "authorized node-agent namespace")
	flag.StringVar(&observationAgentServiceAccount, "observation-agent-service-account", "", "authorized node-agent service account")
	flag.StringVar(&gatewayControllerName, "gateway-controller-name", string(waycontroller.DefaultGatewayControllerName), "immutable VPNGatewayClass controller identity")
	flag.StringVar(&releaseVersion, "release-version", "", "immutable signed release version")
	flag.StringVar(&releaseManifestDigest, "release-manifest-digest", "", "immutable signed release manifest digest")
	flag.StringVar(&conformanceProfile, "conformance-profile", "networking.waycloak.io/Core-v1", "immutable conformance profile identity")
	flag.StringVar(&portForwardRuntimeCA, "port-forward-runtime-ca", "", "CA bundle for per-gateway runtime Services; empty disables Extended port forwarding")
	flag.StringVar(&portForwardRuntimeCert, "port-forward-runtime-client-cert", "", "controller mTLS certificate for gateway runtimes")
	flag.StringVar(&portForwardRuntimeKey, "port-forward-runtime-client-key", "", "controller mTLS private key for gateway runtimes")
	flag.UintVar(&portForwardRuntimePort, "port-forward-runtime-port", 9443, "deterministic gateway runtime Service HTTPS port")
	flag.StringVar(&adapterCA, "adapter-ca", "", "CA bundle for out-of-process WorkloadAdapter Services")
	flag.StringVar(&adapterCert, "adapter-client-cert", "", "controller mTLS certificate for adapter health")
	flag.StringVar(&adapterKey, "adapter-client-key", "", "controller mTLS private key for adapter health")
	flag.UintVar(&adapterPort, "adapter-port", uint(portforward.DefaultAdapterPort), "deterministic WorkloadAdapter Service HTTPS port")
	flag.StringVar(&gatewayEngineImage, "gateway-engine-image", "", "exact default gateway engine image by digest")
	flag.StringVar(&gatewayAgentImage, "gateway-agent-image", "", "exact default gateway agent image by digest")
	flag.StringVar(&gatewayOverlayCIDR, "gateway-overlay-cidr", "", "reviewed default gateway overlay CIDR")
	flag.UintVar(&gatewayVNI, "gateway-vni", 7999, "reviewed default gateway VNI")
	flag.IntVar(&gatewayMTU, "gateway-mtu", 1320, "reviewed default gateway overlay MTU")
	flag.UintVar(&gatewayVXLANPort, "gateway-vxlan-port", 4789, "default gateway VXLAN port")
	flag.UintVar(&gatewayHealthPort, "gateway-health-port", 18080, "default gateway overlay health port")
	options := zap.Options{Development: false}
	options.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&options)))
	if !waycontroller.ValidReleaseIdentity(wayv1.ReleaseIdentity{Version: releaseVersion, ManifestDigest: releaseManifestDigest}) {
		ctrl.Log.Error(nil, "exact signed release version and manifest digest are required")
		os.Exit(1)
	}
	supportedFeatures := wayv1.CoreFeatures()
	var leaseRuntime portforward.Runtime
	var adapterHealth portforward.AdapterHealthChecker
	portForwardConfigured := portForwardRuntimeCA != "" || portForwardRuntimeCert != "" || portForwardRuntimeKey != ""
	if portForwardConfigured {
		if portForwardRuntimeCA == "" || portForwardRuntimeCert == "" || portForwardRuntimeKey == "" || portForwardRuntimePort == 0 || portForwardRuntimePort > 65535 {
			ctrl.Log.Error(nil, "complete port-forward runtime mTLS identity and valid port are required")
			os.Exit(1)
		}
		router, runtimeErr := portforward.NewRuntimeRouter(portForwardRuntimeCA, portForwardRuntimeCert, portForwardRuntimeKey, uint16(portForwardRuntimePort))
		if runtimeErr != nil {
			ctrl.Log.Error(runtimeErr, "configure port-forward runtime routing")
			os.Exit(1)
		}
		leaseRuntime = router
		supportedFeatures = append(supportedFeatures, wayv1.FeaturePortForwardSingleActive)
	}
	adapterConfigured := adapterCA != "" || adapterCert != "" || adapterKey != ""
	if adapterConfigured {
		if adapterCA == "" || adapterCert == "" || adapterKey == "" || adapterPort == 0 || adapterPort > 65535 {
			ctrl.Log.Error(nil, "complete adapter mTLS identity and valid port are required")
			os.Exit(1)
		}
		adapterClient, adapterErr := portforward.NewHTTPAdapterClient(adapterCA, adapterCert, adapterKey, uint16(adapterPort))
		if adapterErr != nil {
			ctrl.Log.Error(adapterErr, "configure WorkloadAdapter health client")
			os.Exit(1)
		}
		adapterHealth = adapterClient
	}
	if portForwardConfigured && adapterConfigured {
		supportedFeatures = append(supportedFeatures, wayv1.FeatureWorkloadAdapter)
	}

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
	var gatewayRuntime waycontroller.GatewayRuntimeProvisioner
	if gatewayEngineImage != "" || gatewayAgentImage != "" || gatewayOverlayCIDR != "" {
		overlay, overlayErr := netip.ParsePrefix(gatewayOverlayCIDR)
		if overlayErr != nil || gatewayEngineImage == "" || gatewayAgentImage == "" || gatewayVNI == 0 || gatewayVNI > 16777215 || gatewayMTU < 576 || gatewayMTU > 9000 || gatewayVXLANPort == 0 || gatewayVXLANPort > 65535 || gatewayHealthPort == 0 || gatewayHealthPort > 65535 {
			ctrl.Log.Error(overlayErr, "complete exact gateway runtime images and network parameters are required")
			os.Exit(1)
		}
		gatewayRuntime = &gatewayruntime.Provisioner{Client: manager.GetClient(), Reader: manager.GetAPIReader(), EngineImage: gatewayEngineImage, AgentImage: gatewayAgentImage, OverlayCIDR: overlay.Masked(), VNI: uint32(gatewayVNI), MTU: int32(gatewayMTU), VXLANPort: uint16(gatewayVXLANPort), HealthPort: uint16(gatewayHealthPort)}
	}
	classController := &waycontroller.VPNGatewayClassReconciler{
		Client: manager.GetClient(), ControllerName: wayv1.ControllerName(gatewayControllerName),
		ReleaseIdentity:    wayv1.ReleaseIdentity{Version: releaseVersion, ManifestDigest: releaseManifestDigest},
		ConformanceProfile: wayv1.QualifiedName(conformanceProfile), SupportedFeatures: supportedFeatures,
	}
	if err = classController.SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "setup VPNGatewayClass controller")
		os.Exit(1)
	}
	if err = (&waycontroller.ReplacementVPNGatewayReconciler{
		Client: manager.GetClient(), APIReader: manager.GetAPIReader(), ControllerName: wayv1.ControllerName(gatewayControllerName),
		ReleaseIdentity:    wayv1.ReleaseIdentity{Version: releaseVersion, ManifestDigest: releaseManifestDigest},
		ConformanceProfile: wayv1.QualifiedName(conformanceProfile), SupportedFeatures: supportedFeatures,
		NativeConfigRoles: []wayv1.QualifiedName{waycontroller.GluetunEnvironmentRole}, CredentialRoles: []wayv1.QualifiedName{waycontroller.OpenVPNCredentialsRole}, Runtime: gatewayRuntime,
	}).SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "setup VPNGateway controller")
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
	if err = (&portforward.PortForwardLeaseReconciler{Client: manager.GetClient(), APIReader: manager.GetAPIReader(), Runtime: leaseRuntime}).SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "setup PortForwardLease controller")
		os.Exit(1)
	}
	if err = (&portforward.WorkloadAdapterReconciler{Client: manager.GetClient(), APIReader: manager.GetAPIReader(), Health: adapterHealth}).SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "setup WorkloadAdapter controller")
		os.Exit(1)
	}
	if err = (&scheduling.NodeCapabilityReconciler{Client: manager.GetClient()}).SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "setup node capability controller")
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
			NodePublisher: &scheduling.Publisher{Client: manager.GetClient(),
				ReleaseIdentity:    wayv1.ReleaseIdentity{Version: releaseVersion, ManifestDigest: releaseManifestDigest},
				ConformanceProfile: wayv1.QualifiedName(conformanceProfile)},
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
