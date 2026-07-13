package main

import (
	"flag"
	"os"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	wayadmission "github.com/Amoenus/waycloak/internal/admission"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=Fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=mpod.networking.waycloak.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-v1-pod,mutating=false,failurePolicy=Fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=vpod.networking.waycloak.io,admissionReviewVersions=v1

func main() {
	var metricsAddr, probeAddr, agentImage, gatewayManagerImage, webhookCertDir string
	var webhookPort int
	var leader, controllersEnabled bool
	var allocationQuarantine time.Duration
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "")
	flag.StringVar(&agentImage, "agent-image", "", "immutable agent image reference injected into protected Pods")
	flag.StringVar(&gatewayManagerImage, "gateway-manager-image", "", "immutable gateway-manager image reference; empty leaves gateway workload reconciliation disabled")
	flag.StringVar(&webhookCertDir, "webhook-cert-dir", "", "directory containing tls.crt and tls.key")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "HTTPS port for admission webhooks")
	flag.BoolVar(&leader, "leader-elect", true, "")
	flag.BoolVar(&controllersEnabled, "controllers-enabled", true, "run reconcilers in addition to admission webhooks")
	flag.DurationVar(&allocationQuarantine, "allocation-quarantine", 5*time.Minute, "delay before a released overlay address may be reused")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")
	if agentImage == "" {
		log.Error(nil, "--agent-image is required")
		os.Exit(2)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(wayv1.AddToScheme(scheme))
	webhookOptions := webhook.Options{Port: webhookPort}
	if webhookCertDir != "" {
		webhookOptions.CertDir = webhookCertDir
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: metricsAddr}, HealthProbeBindAddress: probeAddr, LeaderElection: leader, LeaderElectionID: "waycloak-controller.networking.waycloak.io", WebhookServer: webhook.NewServer(webhookOptions)})
	if err != nil {
		log.Error(err, "create manager")
		os.Exit(1)
	}
	if controllersEnabled {
		//lint:ignore SA1019 controller-runtime has no legacy-recorder adapter yet.
		if err = (&waycontroller.PodReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorderFor("waycloak-pod")}).SetupWithManager(mgr); err != nil {
			log.Error(err, "setup Pod controller")
			os.Exit(1)
		}
		//lint:ignore SA1019 controller-runtime has no legacy-recorder adapter yet.
		if err = (&waycontroller.GatewayReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorderFor("waycloak-gateway"), ManagerImage: gatewayManagerImage}).SetupWithManager(mgr); err != nil {
			log.Error(err, "setup gateway controller")
			os.Exit(1)
		}
		//lint:ignore SA1019 controller-runtime has no legacy-recorder adapter yet.
		if err = (&waycontroller.PortForwardLeaseReconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorderFor("waycloak-port-forward-lease")}).SetupWithManager(mgr); err != nil {
			log.Error(err, "setup port-forward lease controller")
			os.Exit(1)
		}
		if err = (&waycontroller.WorkloadGCReconciler{Client: mgr.GetClient(), Quarantine: allocationQuarantine}).SetupWithManager(mgr); err != nil {
			log.Error(err, "setup workload GC")
			os.Exit(1)
		}
	}
	mgr.GetWebhookServer().Register("/mutate-v1-pod", &cradmission.Webhook{Handler: &wayadmission.PodMutator{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), AgentImage: agentImage}})
	mgr.GetWebhookServer().Register("/validate-v1-pod", &cradmission.Webhook{Handler: &wayadmission.PodValidator{AgentImage: agentImage}})
	_ = corev1.NamespaceDefault
	if err = mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "health check")
		os.Exit(1)
	}
	if err = mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "ready check")
		os.Exit(1)
	}
	if err = mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "run manager")
		os.Exit(1)
	}
}
