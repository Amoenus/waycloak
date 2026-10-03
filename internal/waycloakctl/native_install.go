// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"time"

	installv1 "github.com/Amoenus/waycloak/api/installation/v1alpha1"
	"github.com/Amoenus/waycloak/internal/enrollment"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"
)

const (
	NativePhaseHold     = "Holding"
	NativePhaseClass    = "WithdrawingClass"
	NativePhaseStage    = "Staging"
	NativePhaseGateways = "ReplacingGateways"
	NativePhaseActivate = "Activating"
	NativePhaseVerify   = "Verifying"
	NativePhaseComplete = "Complete"
)

// NativeInstallPlan is an immutable journal snapshot, never an executable user
// request. The installation reconciler verifies the signed target, owns the
// journal and serializes its phases with a Lease before calling this package.
type NativeInstallPlan struct {
	Plan          InstallPlan       `json:"plan"`
	ClusterUID    string            `json:"clusterUID"`
	CAUID         string            `json:"caUID"`
	TLSUID        string            `json:"tlsUID"`
	CADigest      string            `json:"caDigest"`
	ServingDigest string            `json:"servingDigest"`
	Layout        NodeInstallLayout `json:"layout"`
}

func PrepareNativeInstall(ctx context.Context, clients *Clients, runtime InstallRuntime, spec installv1.WaycloakInstallationSpec, manifest ReleaseManifest) (NativeInstallPlan, error) {
	var result NativeInstallPlan
	if manifest.Version != spec.Version {
		return result, errors.New("verified release differs from installation intent")
	}
	if err := manifest.Validate(); err != nil {
		return result, err
	}
	if err := ensureNoCertificateRotation(ctx, clients, spec.Namespace, spec.Release); err != nil {
		return result, err
	}
	if err := ensureNoInstallRepair(ctx, clients, spec.Namespace, spec.Release); err != nil {
		return result, err
	}
	if _, _, found, err := loadInstallTransitionJournal(ctx, clients, spec.Namespace, spec.Release); err != nil || found {
		if err != nil {
			return result, err
		}
		return result, errors.New("finish the existing CLI transition before declarative adoption")
	}
	report, err := Preflight(ctx, clients, spec.OverlayCIDR)
	if err != nil {
		return result, err
	}
	if !report.Compatible {
		return result, errors.New("cluster preflight is incompatible with installation")
	}
	targetCRDs, err := runtime.CRDIdentities(ctx, manifest.Chart)
	if err != nil {
		return result, err
	}
	source, err := ObserveInstalledRelease(ctx, clients, spec.Namespace, spec.Release)
	if err != nil {
		return result, err
	}
	if source.State == installStateDeployed && !spec.AdoptExisting {
		return result, errors.New("existing runtime requires spec.adoptExisting after stopping its previous manager")
	}
	var portForward *PortForwardInstallIdentity
	if spec.Config.PortForwarding != nil {
		configured := spec.Config.PortForwarding
		identity, err := observePortForwardInstallIdentity(ctx, clients, spec.Namespace, configured.ControllerTLSSecret, configured.AdapterEnabled)
		if err != nil {
			return result, err
		}
		portForward = &identity
	}
	plan, err := buildInstallPlan(manifest, spec.Namespace, spec.Release, spec.NodeArchitecture, report, source, targetCRDs, portForward, true)
	if err != nil {
		return result, err
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(plan.Values), &values); err != nil {
		return result, err
	}
	layout := source.NodeLayout
	if layout == nil {
		layout = &NodeInstallLayout{ConfigPath: report.CNI.ConfigPath, BinaryPath: report.CNI.BinaryPath, ReceiptPath: "/var/lib/cni/waycloak/install-receipt.json"}
		// Own a separate chain on fresh K3s installations. K3s remains free to
		// atomically regenerate its primary source during infrastructure upgrades.
		if report.CNI.Name == "flannel" || report.CNI.Name == "k3s-flannel" {
			layout.SourceConfigPath = report.CNI.ConfigPath
			layout.ConfigPath = path.Join(path.Dir(report.CNI.ConfigPath), "05-waycloak.conflist")
		}
		installer := values["cniInstaller"].(map[string]any)
		agent := values["nodeAgent"].(map[string]any)
		installer["configHostPath"], installer["nodeSelector"] = layout.ConfigPath, map[string]any{}
		if layout.SourceConfigPath != "" {
			installer["sourceConfigHostPath"] = layout.SourceConfigPath
		}
		agent["cniConfigHostPath"], agent["nodeSelector"] = layout.ConfigPath, map[string]any{}
	}
	controller := values["controller"].(map[string]any)
	if err := nativeSetResources(controller, spec.Config.ControllerResources); err != nil {
		return result, err
	}
	if err := nativeSetResources(values["nodeAgent"].(map[string]any), spec.Config.NodeAgentResources); err != nil {
		return result, err
	}
	if err := nativeSetResources(values["cniInstaller"].(map[string]any), spec.Config.InstallerResources); err != nil {
		return result, err
	}
	if len(spec.Config.ControllerNodeSelector) > 0 {
		controller["nodeSelector"] = spec.Config.ControllerNodeSelector
	}
	if len(spec.Config.ControllerTolerations) > 0 {
		controller["tolerations"] = spec.Config.ControllerTolerations
	}
	encoded, err := yaml.Marshal(values)
	if err != nil {
		return result, err
	}
	plan.Values = string(encoded)
	plan.PlanID = installPlanIdentity(plan)
	ns, err := clients.Kubernetes.CoreV1().Namespaces().Get(ctx, spec.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		ns, err = clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: spec.Namespace, Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}, metav1.CreateOptions{})
	}
	if err != nil {
		return result, err
	}
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "privileged" {
		return result, errors.New("runtime namespace must allow privileged node components")
	}
	ca, tls, err := ensureObservationSecrets(ctx, clients, spec.Namespace, spec.Release, plan.PlanID)
	if err != nil {
		return result, err
	}
	result = NativeInstallPlan{Plan: plan, ClusterUID: report.Identity.ClusterUIDFingerprint, CAUID: string(ca.UID), TLSUID: string(tls.UID), CADigest: digestBytes(ca.Data["ca.crt"]), ServingDigest: digestBytes(tls.Data["tls.crt"]), Layout: *layout}
	return result, nil
}

func nativeSetResources(values map[string]any, resources corev1.ResourceRequirements) error {
	if len(resources.Claims) > 0 {
		return errors.New("resource claims are not supported by the installation contract")
	}
	if len(resources.Limits) == 0 && len(resources.Requests) == 0 {
		return nil
	}
	data, err := json.Marshal(resources)
	if err != nil {
		return err
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	values["resources"] = decoded
	return nil
}

// AdvanceNativeInstall executes one idempotent phase. The caller persists the
// phase before executing it, and advances only after the returned observation.
// Errors never activate agents, roll back implicitly, or delete the journal.
func AdvanceNativeInstall(ctx context.Context, clients *Clients, runtime InstallRuntime, snapshot NativeInstallPlan, phase string) (string, error) {
	plan := snapshot.Plan
	if err := validateNativeInstall(ctx, clients, runtime, snapshot); err != nil {
		return phase, err
	}
	deployed := plan.Source.State == installStateDeployed
	switch phase {
	case NativePhaseHold:
		if !deployed {
			return NativePhaseStage, nil
		}
		if err := ensureTransitionQuiescence(ctx, clients, plan); err != nil {
			return phase, err
		}
		return NativePhaseClass, nil
	case NativePhaseClass:
		class, err := clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, "gluetun.waycloak.io", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return NativePhaseStage, nil
		}
		if err != nil {
			return phase, err
		}
		if string(class.GetUID()) != plan.Source.GatewayClassUID {
			return phase, errors.New("foreign gateway class appeared during withdrawal")
		}
		if err := waitForTransitionQuiescence(ctx, clients, plan, installCheckpointQuiesced); err != nil {
			return phase, err
		}
		uid := class.GetUID()
		if err := clients.Dynamic.Resource(gatewayClassGVR).Delete(ctx, class.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return phase, err
		}
		return phase, nil // Observe deletion before allowing the target chart.
	case NativePhaseStage:
		overrides := controllerFirstBootstrapValues
		if deployed {
			var err error
			overrides, err = nodeAgentTransitionHoldValues(plan)
			if err != nil {
				return phase, err
			}
		}
		if err := runtime.Apply(ctx, plan, overrides); err != nil {
			return phase, err
		}
		if !deployed {
			return NativePhaseActivate, nil
		}
		if err := nativeStaged(ctx, clients, snapshot); err != nil {
			return phase, err
		}
		return NativePhaseGateways, nil
	case NativePhaseGateways:
		if err := nativeStaged(ctx, clients, snapshot); err != nil {
			return phase, err
		}
		if err := ensureTargetGatewayPods(ctx, clients, plan.Target); err != nil {
			return phase, err
		}
		return NativePhaseActivate, nil
	case NativePhaseActivate:
		// Reobserve gateways even when replaying a partially completed activation.
		if deployed {
			if err := ensureTargetGatewayPods(ctx, clients, plan.Target); err != nil {
				return phase, err
			}
		}
		if err := runtime.Apply(ctx, plan, ""); err != nil {
			return phase, err
		}
		return NativePhaseVerify, nil
	case NativePhaseVerify:
		if err := VerifyNativeInstall(ctx, clients, snapshot); err != nil {
			return phase, err
		}
		return NativePhaseComplete, nil
	default:
		return phase, fmt.Errorf("unknown installation phase %q", phase)
	}
}

func validateNativeInstall(ctx context.Context, clients *Clients, runtime InstallRuntime, snapshot NativeInstallPlan) error {
	plan := snapshot.Plan
	if !validDigest(plan.PlanID) || plan.PlanID != installPlanIdentity(plan) || !validDigest(snapshot.ClusterUID) {
		return errors.New("installation journal identity is invalid")
	}
	if err := plan.Target.Validate(); err != nil {
		return err
	}
	if plan.Chart != plan.Target.Chart || plan.Manifest != plan.Target.ManifestDigest {
		return errors.New("installation artifact identity is inconsistent")
	}
	if err := plan.Source.validate(); err != nil {
		return err
	}
	if err := ensureNoCertificateRotation(ctx, clients, plan.Namespace, plan.Release); err != nil {
		return err
	}
	if err := ensureNoInstallRepair(ctx, clients, plan.Namespace, plan.Release); err != nil {
		return err
	}
	report, err := Preflight(ctx, clients, plan.OverlayCIDR)
	if err != nil {
		return err
	}
	// Kernel and Kubernetes patch changes do not invalidate a durable transition.
	// Cluster identity, supported networking, and the owned CNI paths still do.
	if !report.Compatible || report.Identity.ClusterUIDFingerprint != snapshot.ClusterUID {
		return errors.New("installation no longer targets the same compatible cluster")
	}
	crds, err := runtime.CRDIdentities(ctx, plan.Chart)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(crds, plan.TargetCRDs) {
		return errors.New("verified chart networking API identity changed")
	}
	for name, digest := range crds {
		live, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) && plan.Source.State == installStateAbsent {
			continue
		}
		if err != nil {
			return err
		}
		observed, err := installCRDSpecDigest(live.Spec)
		if err != nil || observed != digest {
			return errors.New("networking API migration is not supported by this transition")
		}
	}
	ca, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.Release+"-observation-ca", metav1.GetOptions{})
	if err != nil {
		return err
	}
	tls, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.Release+"-observation-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(ca.UID) != snapshot.CAUID || string(tls.UID) != snapshot.TLSUID || digestBytes(ca.Data["ca.crt"]) != snapshot.CADigest || digestBytes(tls.Data["tls.crt"]) != snapshot.ServingDigest {
		return errors.New("stable observation certificate identity changed")
	}
	if plan.PortForwarding != nil {
		identity, err := observePortForwardInstallIdentity(ctx, clients, plan.Namespace, plan.PortForwarding.ControllerTLSSecret, plan.PortForwarding.AdapterProtocolEnabled)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(identity, *plan.PortForwarding) {
			return errors.New("port-forward trust changed during transition")
		}
	}
	return nil
}

func nativeStaged(ctx context.Context, clients *Clients, snapshot NativeInstallPlan) error {
	plan := snapshot.Plan
	components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return err
	}
	if !sameTransitionTrust(components, plan.Source) || !components.ClassPresent || components.ClassUID == plan.Source.GatewayClassUID ||
		components.ClassVersion != plan.Target.Version || components.ClassManifest != plan.Target.ManifestDigest ||
		!components.ObservationCapabilityHeld || components.ObservationCapabilityHoldID != plan.PlanID || components.TransitionPlanID != plan.PlanID ||
		components.NodeAgentVersion != plan.Source.Version || components.NodeAgentManifest != plan.Source.ManifestDigest ||
		components.ControllerVersion != plan.Target.Version || components.ControllerManifest != plan.Target.ManifestDigest ||
		components.CNIVersion != plan.Target.Version || components.CNIManifest != plan.Target.ManifestDigest || !reflect.DeepEqual(components.CRDIdentities, plan.TargetCRDs) {
		return errors.New("runtime has not reached the exact held target")
	}
	if plan.Source.ManifestDigest != plan.Target.ManifestDigest {
		if components.ControllerTransitionPlanID != plan.PlanID || components.ControllerTransitionSourceVersion != plan.Source.Version || components.ControllerTransitionSourceManifest != plan.Source.ManifestDigest {
			return errors.New("held controller does not acknowledge the exact source transition")
		}
	} else if components.ControllerTransitionPlanID != "" || components.ControllerTransitionSourceVersion != "" || components.ControllerTransitionSourceManifest != "" {
		return errors.New("configuration transition contains a foreign controller source identity")
	}
	for _, name := range installRuntimeImageNames {
		artifact := plan.Target.Images[name]
		if components.Images[name] != artifact.Repository+"@"+artifact.Digest {
			return fmt.Errorf("held target has unexpected %s image", name)
		}
	}
	bindings, err := clients.Dynamic.Resource(transitionBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for index := range bindings.Items {
		if !bindingAcknowledgesTransitionDeny(&bindings.Items[index], plan.PlanID) {
			return errors.New("held target is waiting for authenticated attachment denial")
		}
	}
	return nil
}

func VerifyNativeInstall(ctx context.Context, clients *Clients, snapshot NativeInstallPlan) error {
	plan := snapshot.Plan
	target, err := ObserveInstalledRelease(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return err
	}
	if err := validateInstallTargetWithClassReplacement(plan.Source, target, plan.Target, plan.TargetCRDs, plan.Source.State == installStateDeployed); err != nil {
		return err
	}
	if !reflect.DeepEqual(target.NodeLayout, &snapshot.Layout) {
		return errors.New("target changed owned CNI layout or node coverage")
	}
	if target.ObservationCAUID != snapshot.CAUID || target.ObservationTLSUID != snapshot.TLSUID || target.ObservationCADigest != snapshot.CADigest || target.ObservationServingDigest != snapshot.ServingDigest {
		return errors.New("target changed stable observation trust")
	}
	if err := ensureTargetGatewayPods(ctx, clients, plan.Target); err != nil {
		return err
	}
	return wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		controller, err := clients.Kubernetes.AppsV1().Deployments(plan.Namespace).Get(ctx, chartFullname(plan.Release)+"-controller", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if controller.Spec.Replicas == nil || *controller.Spec.Replicas < 1 || controller.Status.ObservedGeneration < controller.Generation || controller.Status.UpdatedReplicas != *controller.Spec.Replicas || controller.Status.AvailableReplicas != *controller.Spec.Replicas {
			return false, nil
		}
		for _, name := range []string{"-node-agent", "-cni-installer"} {
			ds, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, chartFullname(plan.Release)+name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if ds.Status.DesiredNumberScheduled < 1 || ds.Status.ObservedGeneration < ds.Generation || ds.Status.UpdatedNumberScheduled != ds.Status.DesiredNumberScheduled || ds.Status.NumberReady != ds.Status.DesiredNumberScheduled || ds.Status.NumberUnavailable != 0 {
				return false, nil
			}
		}
		bindings, err := clients.Dynamic.Resource(transitionBindingGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		pods, err := clients.Kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		live := map[types.UID]bool{}
		selected := map[types.UID]bool{}
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
				live[pod.UID] = true
				if pod.Spec.NodeName != "" && pod.Labels[enrollment.RouteLabel] != "" {
					selected[pod.UID] = true
				}
			}
		}
		for index := range bindings.Items {
			binding := &bindings.Items[index]
			uid, _, _ := unstructured.NestedString(binding.Object, "spec", "podRef", "uid")
			if live[types.UID(uid)] && !resourceCurrentReady(binding) {
				return false, nil
			}
			delete(selected, types.UID(uid))
		}
		return len(selected) == 0, nil
	})
}
