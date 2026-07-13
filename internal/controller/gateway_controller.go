package controller

import (
	"context"
	"net/netip"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=networking.waycloak.io,resources=vpngateways/status,verbs=get;update;patch
type GatewayReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var gw wayv1.VPNGateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	p, err := netip.ParsePrefix(gw.Spec.Overlay.CIDR)
	if err != nil || !p.Addr().Is4() || p.Bits() > 30 {
		waystatus.Set(&gw.Status.Conditions, gw.Generation, waystatus.ConditionAccepted, metav1.ConditionFalse, waystatus.ReasonInvalidOverlay, "overlay.cidr must be an IPv4 prefix with client capacity")
	} else {
		waystatus.Set(&gw.Status.Conditions, gw.Generation, waystatus.ConditionAccepted, metav1.ConditionTrue, waystatus.ReasonAccepted, "Gateway control-plane specification is accepted")
	}
	waystatus.Set(&gw.Status.Conditions, gw.Generation, waystatus.ConditionReady, metav1.ConditionFalse, waystatus.ReasonDataPlaneNotImplemented, "The Phase 1 control plane does not implement or observe a VPN data plane")
	gw.Status.ObservedGeneration = gw.Generation
	if err := r.Status().Update(ctx, &gw); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&wayv1.VPNGateway{}).Complete(r)
}
