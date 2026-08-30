// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	installOperationClean      = "CleanInstall"
	installOperationTransition = "ExactReleaseTransition"
	installStateAbsent         = "Absent"
	installStateDeployed       = "Deployed"
	installReleaseOwnerKey     = "install.waycloak.io/release"
	installInitialPlanKey      = "install.waycloak.io/initial-plan-id"
	installTransitionPlanKey   = "install.waycloak.io/transition-plan-id"
	installTransitionPlanData  = "plan.json"
	observationRotationKey     = "install.waycloak.io/observation-rotation-id"
	initialObservationRotation = "initial"
)

const (
	installCheckpointSource                 = "Source"
	installCheckpointQuiesced               = "Quiesced"
	installCheckpointClassWithdrawn         = "ClassWithdrawn"
	installCheckpointQuiescedClassWithdrawn = "QuiescedClassWithdrawn"
	installCheckpointClassReplaced          = "ClassReplaced"
	installCheckpointQuiescedClassReplaced  = "QuiescedClassReplaced"
	installCheckpointStaged                 = "Staged"
	installCheckpointTarget                 = "Target"
)

var installRuntimeImageNames = []string{
	"replacement-controller",
	"waycloak-cni",
	"waycloak-node-agent",
	"waycloak-gateway-agent",
	"gluetun",
	"pause",
}

// InstalledReleaseObservation is the non-sensitive, canonical source state
// bound into every install or exact-release transition plan.
type InstalledReleaseObservation struct {
	State                    string            `json:"state"`
	ObservationDigest        string            `json:"observationDigest"`
	HelmRevision             int64             `json:"helmRevision,omitempty"`
	Version                  string            `json:"version,omitempty"`
	ManifestDigest           string            `json:"manifestDigest,omitempty"`
	Images                   map[string]string `json:"images,omitempty"`
	GatewayClassUID          string            `json:"gatewayClassUID,omitempty"`
	GatewayClassGeneration   int64             `json:"gatewayClassGeneration,omitempty"`
	ObservationCAUID         string            `json:"observationCAUID,omitempty"`
	ObservationTLSUID        string            `json:"observationTLSUID,omitempty"`
	ObservationCADigest      string            `json:"observationCADigest,omitempty"`
	ObservationServingDigest string            `json:"observationServingDigest,omitempty"`
	ObservationRotationID    string            `json:"observationRotationID,omitempty"`
	CRDIdentities            map[string]string `json:"crdIdentities,omitempty"`
}

type deployedReleaseComponents struct {
	HelmRevision                int64
	ControllerVersion           string
	ControllerManifest          string
	CNIVersion                  string
	CNIManifest                 string
	NodeAgentVersion            string
	NodeAgentManifest           string
	ClassPresent                bool
	ClassVersion                string
	ClassManifest               string
	ClassUID                    string
	ClassGeneration             int64
	Images                      map[string]string
	ObservationCAUID            string
	ObservationTLSUID           string
	ObservationCADigest         string
	ObservationServingDigest    string
	ObservationRotationID       string
	ObservationCapabilityHeld   bool
	ObservationCapabilityHoldID string
	TransitionPlanID            string
	CRDIdentities               map[string]string
}

func ObserveInstalledRelease(ctx context.Context, clients *Clients, namespace, release string) (InstalledReleaseObservation, error) {
	if namespace == "" || release == "" {
		return InstalledReleaseObservation{}, errors.New("release observation requires namespace and release")
	}
	crds, err := optionalInstallCRDIdentities(ctx, clients)
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	selector := labels.Set{"owner": "helm", "name": release, "status": "deployed"}.AsSelector().String()
	releases, err := clients.Kubernetes.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil && !apierrors.IsNotFound(err) {
		return InstalledReleaseObservation{}, fmt.Errorf("inspect deployed Helm revision: %w", err)
	}
	if len(releases.Items) > 1 {
		return InstalledReleaseObservation{}, errors.New("multiple deployed Helm revisions make release identity ambiguous")
	}
	if len(releases.Items) == 0 {
		if err := refusePartialInstallState(ctx, clients, namespace, release); err != nil {
			return InstalledReleaseObservation{}, err
		}
		observation := InstalledReleaseObservation{State: installStateAbsent, CRDIdentities: crds}
		return finalizeInstalledReleaseObservation(observation)
	}

	revision, err := strconv.ParseInt(releases.Items[0].Labels["version"], 10, 64)
	if err != nil || revision < 1 {
		return InstalledReleaseObservation{}, errors.New("deployed Helm revision lacks a valid immutable revision number")
	}
	if len(crds) != len(stateCRDNames) {
		return InstalledReleaseObservation{}, errors.New("deployed release lacks the exact replacement CRD inventory")
	}
	name := chartFullname(release)
	controller, err := clients.Kubernetes.AppsV1().Deployments(namespace).Get(ctx, name+"-controller", metav1.GetOptions{})
	if err != nil {
		return InstalledReleaseObservation{}, fmt.Errorf("observe deployed controller: %w", err)
	}
	cni, err := clients.Kubernetes.AppsV1().DaemonSets(namespace).Get(ctx, name+"-cni-installer", metav1.GetOptions{})
	if err != nil {
		return InstalledReleaseObservation{}, fmt.Errorf("observe deployed CNI installer: %w", err)
	}
	agent, err := clients.Kubernetes.AppsV1().DaemonSets(namespace).Get(ctx, name+"-node-agent", metav1.GetOptions{})
	if err != nil {
		return InstalledReleaseObservation{}, fmt.Errorf("observe deployed node agent: %w", err)
	}
	class, err := clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, "gluetun.waycloak.io", metav1.GetOptions{})
	if err != nil {
		return InstalledReleaseObservation{}, fmt.Errorf("observe default gateway class: %w", err)
	}
	caSecret, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, release+"-observation-ca", metav1.GetOptions{})
	if err != nil {
		return InstalledReleaseObservation{}, fmt.Errorf("observe installation CA: %w", err)
	}
	tlsSecret, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, release+"-observation-tls", metav1.GetOptions{})
	if err != nil {
		return InstalledReleaseObservation{}, fmt.Errorf("observe installation serving identity: %w", err)
	}
	if err := validateReleaseOwnedInstallSecret(caSecret, release, corev1.SecretTypeOpaque); err != nil {
		return InstalledReleaseObservation{}, err
	}
	if err := validateReleaseOwnedInstallSecret(tlsSecret, release, corev1.SecretTypeTLS); err != nil {
		return InstalledReleaseObservation{}, err
	}
	if !bytes.Equal(caSecret.Data["ca.crt"], tlsSecret.Data["ca.crt"]) {
		return InstalledReleaseObservation{}, errors.New("observation CA and serving identity do not share exact trust material")
	}

	controllerContainer, err := requiredContainer(controller.Spec.Template.Spec.Containers, "controller")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	cniContainer, err := requiredContainer(cni.Spec.Template.Spec.InitContainers, "install")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	pauseContainer, err := requiredContainer(cni.Spec.Template.Spec.Containers, "receipt-holder")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	agentContainer, err := requiredContainer(agent.Spec.Template.Spec.Containers, "node-agent")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	version, err := requiredArgument(controllerContainer.Args, "--release-version=")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	manifestDigest, err := requiredArgument(controllerContainer.Args, "--release-manifest-digest=")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	engineImage, err := requiredArgument(controllerContainer.Args, "--gateway-engine-image=")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	gatewayAgentImage, err := requiredArgument(controllerContainer.Args, "--gateway-agent-image=")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	nodeVersion, err := requiredArgument(agentContainer.Args, "--release-version=")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	nodeManifest, err := requiredArgument(agentContainer.Args, "--release-manifest-digest=")
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	capabilityHeld, err := observationCapabilityHeld(agentContainer.Args)
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	if capabilityHeld {
		return InstalledReleaseObservation{}, errors.New("deployed node agent retains an observation capability hold")
	}
	capabilityHoldID, err := observationCapabilityHoldID(agentContainer.Args)
	if err != nil {
		return InstalledReleaseObservation{}, err
	}
	if capabilityHoldID != "" || agent.Spec.Template.Annotations[installTransitionPlanKey] != "" {
		return InstalledReleaseObservation{}, errors.New("deployed node agent retains release-transition authority")
	}
	if len(cniContainer.Args) < 2 {
		return InstalledReleaseObservation{}, errors.New("CNI installer lacks release identity arguments")
	}
	rotationID := agent.Spec.Template.Annotations[observationRotationKey]
	if rotationID == "" {
		rotationID = initialObservationRotation
	}
	cniVersion := cniContainer.Args[len(cniContainer.Args)-2]
	cniManifest := cniContainer.Args[len(cniContainer.Args)-1]
	classVersion, _, _ := unstructured.NestedString(class.Object, "spec", "releaseIdentity", "version")
	classManifest, _, _ := unstructured.NestedString(class.Object, "spec", "releaseIdentity", "manifestDigest")
	if version == "" || !validDigest(manifestDigest) || nodeVersion != version || cniVersion != version || classVersion != version || nodeManifest != manifestDigest || cniManifest != manifestDigest || classManifest != manifestDigest {
		return InstalledReleaseObservation{}, errors.New("deployed controller, CNI, node agent, and gateway class release identities disagree")
	}
	images := map[string]string{
		"replacement-controller": controllerContainer.Image,
		"waycloak-cni":           cniContainer.Image,
		"waycloak-node-agent":    agentContainer.Image,
		"waycloak-gateway-agent": gatewayAgentImage,
		"gluetun":                engineImage,
		"pause":                  pauseContainer.Image,
	}
	for _, imageName := range installRuntimeImageNames {
		if !validExactImageReference(images[imageName]) {
			return InstalledReleaseObservation{}, fmt.Errorf("deployed %s image is not digest resolved", imageName)
		}
	}
	observation := InstalledReleaseObservation{
		State: installStateDeployed, HelmRevision: revision, Version: version, ManifestDigest: manifestDigest,
		Images: images, GatewayClassUID: string(class.GetUID()), GatewayClassGeneration: class.GetGeneration(),
		ObservationCAUID: string(caSecret.UID), ObservationTLSUID: string(tlsSecret.UID),
		ObservationCADigest: digestBytes(caSecret.Data["ca.crt"]), ObservationServingDigest: digestBytes(tlsSecret.Data["tls.crt"]),
		ObservationRotationID: rotationID,
		CRDIdentities:         crds,
	}
	return finalizeInstalledReleaseObservation(observation)
}

func observeDeployedReleaseComponents(ctx context.Context, clients *Clients, namespace, release string) (deployedReleaseComponents, error) {
	var observation deployedReleaseComponents
	crds, err := optionalInstallCRDIdentities(ctx, clients)
	if err != nil {
		return observation, err
	}
	if len(crds) != len(stateCRDNames) {
		return observation, errors.New("transition checkpoint lacks the exact replacement CRD inventory")
	}
	selector := labels.Set{"owner": "helm", "name": release, "status": "deployed"}.AsSelector().String()
	releases, err := clients.Kubernetes.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return observation, fmt.Errorf("inspect transition Helm revision: %w", err)
	}
	if len(releases.Items) != 1 {
		return observation, errors.New("transition checkpoint requires one unambiguous deployed Helm revision")
	}
	revision, err := strconv.ParseInt(releases.Items[0].Labels["version"], 10, 64)
	if err != nil || revision < 1 {
		return observation, errors.New("transition checkpoint lacks a valid deployed Helm revision")
	}

	name := chartFullname(release)
	controller, err := clients.Kubernetes.AppsV1().Deployments(namespace).Get(ctx, name+"-controller", metav1.GetOptions{})
	if err != nil {
		return observation, fmt.Errorf("observe transition controller: %w", err)
	}
	cni, err := clients.Kubernetes.AppsV1().DaemonSets(namespace).Get(ctx, name+"-cni-installer", metav1.GetOptions{})
	if err != nil {
		return observation, fmt.Errorf("observe transition CNI installer: %w", err)
	}
	agent, err := clients.Kubernetes.AppsV1().DaemonSets(namespace).Get(ctx, name+"-node-agent", metav1.GetOptions{})
	if err != nil {
		return observation, fmt.Errorf("observe transition node agent: %w", err)
	}
	class, classErr := clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, "gluetun.waycloak.io", metav1.GetOptions{})
	if classErr != nil && !apierrors.IsNotFound(classErr) {
		return observation, fmt.Errorf("observe transition gateway class: %w", classErr)
	}
	caSecret, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, release+"-observation-ca", metav1.GetOptions{})
	if err != nil {
		return observation, fmt.Errorf("observe transition installation CA: %w", err)
	}
	tlsSecret, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, release+"-observation-tls", metav1.GetOptions{})
	if err != nil {
		return observation, fmt.Errorf("observe transition serving identity: %w", err)
	}
	if err := validateReleaseOwnedInstallSecret(caSecret, release, corev1.SecretTypeOpaque); err != nil {
		return observation, err
	}
	if err := validateReleaseOwnedInstallSecret(tlsSecret, release, corev1.SecretTypeTLS); err != nil {
		return observation, err
	}
	controllerContainer, err := requiredContainer(controller.Spec.Template.Spec.Containers, "controller")
	if err != nil {
		return observation, err
	}
	cniContainer, err := requiredContainer(cni.Spec.Template.Spec.InitContainers, "install")
	if err != nil {
		return observation, err
	}
	pauseContainer, err := requiredContainer(cni.Spec.Template.Spec.Containers, "receipt-holder")
	if err != nil {
		return observation, err
	}
	agentContainer, err := requiredContainer(agent.Spec.Template.Spec.Containers, "node-agent")
	if err != nil {
		return observation, err
	}
	controllerVersion, err := requiredArgument(controllerContainer.Args, "--release-version=")
	if err != nil {
		return observation, err
	}
	controllerManifest, err := requiredArgument(controllerContainer.Args, "--release-manifest-digest=")
	if err != nil {
		return observation, err
	}
	engineImage, err := requiredArgument(controllerContainer.Args, "--gateway-engine-image=")
	if err != nil {
		return observation, err
	}
	gatewayAgentImage, err := requiredArgument(controllerContainer.Args, "--gateway-agent-image=")
	if err != nil {
		return observation, err
	}
	nodeVersion, err := requiredArgument(agentContainer.Args, "--release-version=")
	if err != nil {
		return observation, err
	}
	nodeManifest, err := requiredArgument(agentContainer.Args, "--release-manifest-digest=")
	if err != nil {
		return observation, err
	}
	capabilityHeld, err := observationCapabilityHeld(agentContainer.Args)
	if err != nil {
		return observation, err
	}
	capabilityHoldID, err := observationCapabilityHoldID(agentContainer.Args)
	if err != nil {
		return observation, err
	}
	if len(cniContainer.Args) < 2 {
		return observation, errors.New("transition CNI installer lacks release identity arguments")
	}
	rotationID := agent.Spec.Template.Annotations[observationRotationKey]
	if rotationID == "" {
		rotationID = initialObservationRotation
	}
	cniVersion := cniContainer.Args[len(cniContainer.Args)-2]
	cniManifest := cniContainer.Args[len(cniContainer.Args)-1]
	images := map[string]string{
		"replacement-controller": controllerContainer.Image,
		"waycloak-cni":           cniContainer.Image,
		"waycloak-node-agent":    agentContainer.Image,
		"waycloak-gateway-agent": gatewayAgentImage,
		"gluetun":                engineImage,
		"pause":                  pauseContainer.Image,
	}
	for _, imageName := range installRuntimeImageNames {
		if !validExactImageReference(images[imageName]) {
			return observation, fmt.Errorf("transition checkpoint %s image is not digest resolved", imageName)
		}
	}

	observation = deployedReleaseComponents{
		HelmRevision: revision, ControllerVersion: controllerVersion, ControllerManifest: controllerManifest,
		CNIVersion: cniVersion, CNIManifest: cniManifest, NodeAgentVersion: nodeVersion, NodeAgentManifest: nodeManifest,
		Images: images, ObservationCAUID: string(caSecret.UID), ObservationTLSUID: string(tlsSecret.UID),
		ObservationCADigest: digestBytes(caSecret.Data["ca.crt"]), ObservationServingDigest: digestBytes(tlsSecret.Data["tls.crt"]),
		ObservationRotationID:       rotationID,
		ObservationCapabilityHeld:   capabilityHeld,
		ObservationCapabilityHoldID: capabilityHoldID,
		TransitionPlanID:            agent.Spec.Template.Annotations[installTransitionPlanKey],
		CRDIdentities:               crds,
	}
	if classErr == nil {
		observation.ClassPresent = true
		observation.ClassVersion, _, _ = unstructured.NestedString(class.Object, "spec", "releaseIdentity", "version")
		observation.ClassManifest, _, _ = unstructured.NestedString(class.Object, "spec", "releaseIdentity", "manifestDigest")
		observation.ClassUID = string(class.GetUID())
		observation.ClassGeneration = class.GetGeneration()
	}
	return observation, nil
}

func classifyInstallTransitionCheckpoint(components deployedReleaseComponents, plan InstallPlan) (string, error) {
	if exactSourceComponents(components, plan.Source, false) {
		return installCheckpointSource, nil
	}
	if exactQuiescedComponents(components, plan, false) {
		return installCheckpointQuiesced, nil
	}
	if exactSourceComponents(components, plan.Source, true) {
		return installCheckpointClassWithdrawn, nil
	}
	if exactQuiescedComponents(components, plan, true) {
		return installCheckpointQuiescedClassWithdrawn, nil
	}
	if exactClassReplacedComponents(components, plan.Source, plan.Target) {
		return installCheckpointClassReplaced, nil
	}
	if exactQuiescedClassReplacedComponents(components, plan) {
		return installCheckpointQuiescedClassReplaced, nil
	}
	if exactStagedComponents(components, plan, plan.TargetCRDs) {
		return installCheckpointStaged, nil
	}
	return "", errors.New("release state does not match a journal-bound exact transition checkpoint")
}

func exactQuiescedComponents(components deployedReleaseComponents, plan InstallPlan, classWithdrawn bool) bool {
	if !exactQuiescedRuntimeComponents(components, plan) {
		return false
	}
	if classWithdrawn {
		return !components.ClassPresent
	}
	return components.ClassPresent && components.ClassVersion == plan.Source.Version &&
		components.ClassManifest == plan.Source.ManifestDigest && components.ClassUID == plan.Source.GatewayClassUID &&
		components.ClassGeneration == plan.Source.GatewayClassGeneration
}

func exactQuiescedClassReplacedComponents(components deployedReleaseComponents, plan InstallPlan) bool {
	return exactQuiescedRuntimeComponents(components, plan) && components.ClassPresent &&
		components.ClassVersion == plan.Target.Version && components.ClassManifest == plan.Target.ManifestDigest &&
		components.ClassUID != "" && components.ClassUID != plan.Source.GatewayClassUID && components.ClassGeneration >= 1
}

func exactQuiescedRuntimeComponents(components deployedReleaseComponents, plan InstallPlan) bool {
	if plan.Source.State != installStateDeployed || components.HelmRevision != plan.Source.HelmRevision ||
		!components.ObservationCapabilityHeld || components.ObservationCapabilityHoldID != plan.PlanID || components.TransitionPlanID != plan.PlanID ||
		components.ControllerVersion != plan.Source.Version || components.ControllerManifest != plan.Source.ManifestDigest ||
		components.CNIVersion != plan.Source.Version || components.CNIManifest != plan.Source.ManifestDigest ||
		components.NodeAgentVersion != plan.Source.Version || components.NodeAgentManifest != plan.Source.ManifestDigest ||
		components.ObservationRotationID != plan.Source.ObservationRotationID || !sameTransitionTrust(components, plan.Source) ||
		!reflect.DeepEqual(components.CRDIdentities, plan.Source.CRDIdentities) {
		return false
	}
	for _, name := range installRuntimeImageNames {
		wanted := plan.Source.Images[name]
		if name == "waycloak-node-agent" {
			artifact, ok := plan.Target.Images[name]
			if !ok {
				return false
			}
			wanted = artifact.Repository + "@" + artifact.Digest
		}
		if components.Images[name] != wanted {
			return false
		}
	}
	return true
}

func exactSourceComponents(components deployedReleaseComponents, source InstalledReleaseObservation, classWithdrawn bool) bool {
	if !exactSourceRuntimeComponents(components, source) {
		return false
	}
	if classWithdrawn {
		return !components.ClassPresent
	}
	return components.ClassPresent && components.ClassVersion == source.Version && components.ClassManifest == source.ManifestDigest &&
		components.ClassUID == source.GatewayClassUID && components.ClassGeneration == source.GatewayClassGeneration
}

func exactSourceRuntimeComponents(components deployedReleaseComponents, source InstalledReleaseObservation) bool {
	return source.State == installStateDeployed && components.HelmRevision == source.HelmRevision &&
		!components.ObservationCapabilityHeld && components.ObservationCapabilityHoldID == "" && components.TransitionPlanID == "" &&
		components.ControllerVersion == source.Version && components.ControllerManifest == source.ManifestDigest &&
		components.CNIVersion == source.Version && components.CNIManifest == source.ManifestDigest &&
		components.NodeAgentVersion == source.Version && components.NodeAgentManifest == source.ManifestDigest &&
		components.ObservationRotationID == source.ObservationRotationID &&
		reflect.DeepEqual(components.Images, source.Images) && sameTransitionTrust(components, source) &&
		reflect.DeepEqual(components.CRDIdentities, source.CRDIdentities)
}

func exactClassReplacedComponents(components deployedReleaseComponents, source InstalledReleaseObservation, target ReleaseManifest) bool {
	return exactSourceRuntimeComponents(components, source) && components.ClassPresent &&
		components.ClassVersion == target.Version && components.ClassManifest == target.ManifestDigest &&
		components.ClassUID != "" && components.ClassUID != source.GatewayClassUID && components.ClassGeneration >= 1
}

func exactStagedComponents(components deployedReleaseComponents, plan InstallPlan, targetCRDs map[string]string) bool {
	source, target := plan.Source, plan.Target
	if source.State != installStateDeployed || components.HelmRevision <= source.HelmRevision || !components.ClassPresent ||
		!components.ObservationCapabilityHeld || components.ObservationCapabilityHoldID != plan.PlanID || components.TransitionPlanID != plan.PlanID ||
		components.ControllerVersion != target.Version || components.ControllerManifest != target.ManifestDigest ||
		components.CNIVersion != target.Version || components.CNIManifest != target.ManifestDigest ||
		components.NodeAgentVersion != source.Version || components.NodeAgentManifest != source.ManifestDigest ||
		components.ObservationRotationID != source.ObservationRotationID ||
		components.ClassVersion != target.Version || components.ClassManifest != target.ManifestDigest ||
		components.ClassUID == "" || components.ClassGeneration < 1 || !sameTransitionTrust(components, source) ||
		!reflect.DeepEqual(components.CRDIdentities, targetCRDs) {
		return false
	}
	for _, name := range installRuntimeImageNames {
		wanted := target.Images[name].Repository + "@" + target.Images[name].Digest
		if components.Images[name] != wanted {
			return false
		}
	}
	return true
}

func sameTransitionTrust(components deployedReleaseComponents, source InstalledReleaseObservation) bool {
	return components.ObservationCAUID == source.ObservationCAUID && components.ObservationTLSUID == source.ObservationTLSUID &&
		components.ObservationCADigest == source.ObservationCADigest && components.ObservationServingDigest == source.ObservationServingDigest
}

func ChartCRDIdentities(ctx context.Context, runner func(context.Context, string, ...string) ([]byte, error), chart Artifact) (map[string]string, error) {
	if runner == nil {
		runner = defaultRunner
	}
	if chart.Repository == "" || !validDigest(chart.Digest) {
		return nil, errors.New("target chart identity is incomplete")
	}
	output, err := runner(ctx, "helm", "show", "crds", chart.Repository+"@"+chart.Digest)
	if err != nil {
		return nil, fmt.Errorf("read exact target chart CRDs: %w: %s", err, bounded(output, 4096))
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(output), 64<<10)
	identities := make(map[string]string, len(stateCRDNames))
	for {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := decoder.Decode(&crd); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode exact target chart CRDs: %w", err)
		}
		if crd.Name == "" {
			continue
		}
		if crd.APIVersion != apiextensionsv1.SchemeGroupVersion.String() || crd.Kind != "CustomResourceDefinition" || !contains(stateCRDNames, crd.Name) {
			return nil, fmt.Errorf("target chart contains unexpected CRD document %s %s", crd.Kind, crd.Name)
		}
		if _, duplicate := identities[crd.Name]; duplicate {
			return nil, fmt.Errorf("target chart repeats CRD %s", crd.Name)
		}
		digest, err := installCRDSpecDigest(crd.Spec)
		if err != nil {
			return nil, err
		}
		identities[crd.Name] = digest
	}
	if err := validateInstallCRDInventory(identities); err != nil {
		return nil, err
	}
	return identities, nil
}

func validateInstallCRDTransition(source InstalledReleaseObservation, target map[string]string) error {
	if err := validateInstallCRDInventory(target); err != nil {
		return err
	}
	if len(source.CRDIdentities) != 0 && !reflect.DeepEqual(source.CRDIdentities, target) {
		return errors.New("target CRD identity differs from the reviewed live v1beta1 contract; an explicit storage migration plan is required")
	}
	return nil
}

func validateInstallTarget(source, target InstalledReleaseObservation, manifest ReleaseManifest, targetCRDs map[string]string) error {
	if target.State != installStateDeployed || target.Version != manifest.Version || target.ManifestDigest != manifest.ManifestDigest || !reflect.DeepEqual(target.CRDIdentities, targetCRDs) {
		return errors.New("helm completed without the exact target release and CRD identity")
	}
	for _, name := range installRuntimeImageNames {
		wanted := manifest.Images[name].Repository + "@" + manifest.Images[name].Digest
		if target.Images[name] != wanted {
			return fmt.Errorf("helm completed with unexpected %s image", name)
		}
	}
	if target.HelmRevision <= source.HelmRevision {
		return errors.New("helm completed without advancing the deployed revision")
	}
	if source.State == installStateDeployed {
		classChanged := source.ManifestDigest != manifest.ManifestDigest
		if classChanged && target.GatewayClassUID == source.GatewayClassUID {
			return errors.New("release transition did not replace the immutable gateway class identity")
		}
		if !classChanged && target.GatewayClassUID != source.GatewayClassUID {
			return errors.New("same-release apply unexpectedly replaced the gateway class identity")
		}
		if target.ObservationCAUID != source.ObservationCAUID || target.ObservationTLSUID != source.ObservationTLSUID || target.ObservationCADigest != source.ObservationCADigest || target.ObservationServingDigest != source.ObservationServingDigest || target.ObservationRotationID != source.ObservationRotationID {
			return errors.New("ordinary release transition replaced stable observation certificate identity")
		}
	}
	return nil
}

func finalizeInstalledReleaseObservation(observation InstalledReleaseObservation) (InstalledReleaseObservation, error) {
	observation.ObservationDigest = ""
	data, err := json.Marshal(observation)
	if err != nil {
		return observation, err
	}
	observation.ObservationDigest = digestBytes(data)
	return observation, observation.validate()
}

func (observation InstalledReleaseObservation) validate() error {
	if !validDigest(observation.ObservationDigest) {
		return errors.New("installed release observation lacks canonical identity")
	}
	wanted, err := finalizeInstalledReleaseObservationWithoutValidation(observation)
	if err != nil || wanted != observation.ObservationDigest {
		return errors.New("installed release observation content does not match its digest")
	}
	if observation.State == installStateAbsent {
		if observation.HelmRevision != 0 || observation.Version != "" || observation.ManifestDigest != "" || len(observation.Images) != 0 || observation.GatewayClassUID != "" || observation.ObservationCAUID != "" || observation.ObservationTLSUID != "" || observation.ObservationRotationID != "" {
			return errors.New("absent release observation contains deployed state")
		}
		if len(observation.CRDIdentities) != 0 {
			return validateInstallCRDInventory(observation.CRDIdentities)
		}
		return nil
	}
	if observation.State != installStateDeployed || observation.HelmRevision < 1 || observation.Version == "" || !validDigest(observation.ManifestDigest) || observation.GatewayClassUID == "" || observation.GatewayClassGeneration < 1 || observation.ObservationCAUID == "" || observation.ObservationTLSUID == "" || !validDigest(observation.ObservationCADigest) || !validDigest(observation.ObservationServingDigest) || observation.ObservationRotationID != initialObservationRotation && !validDigest(observation.ObservationRotationID) {
		return errors.New("deployed release observation is incomplete")
	}
	if len(observation.Images) != len(installRuntimeImageNames) {
		return errors.New("deployed release observation has an incomplete image inventory")
	}
	for _, name := range installRuntimeImageNames {
		if !validExactImageReference(observation.Images[name]) {
			return fmt.Errorf("deployed release observation lacks exact %s image", name)
		}
	}
	return validateInstallCRDInventory(observation.CRDIdentities)
}

func finalizeInstalledReleaseObservationWithoutValidation(observation InstalledReleaseObservation) (string, error) {
	observation.ObservationDigest = ""
	data, err := json.Marshal(observation)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func optionalInstallCRDIdentities(ctx context.Context, clients *Clients) (map[string]string, error) {
	identities := make(map[string]string, len(stateCRDNames))
	missing := 0
	for _, name := range stateCRDNames {
		crd, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			missing++
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("observe replacement CRD %s: %w", name, err)
		}
		digest, err := installCRDSpecDigest(crd.Spec)
		if err != nil {
			return nil, err
		}
		identities[name] = digest
	}
	if missing == len(stateCRDNames) {
		return map[string]string{}, nil
	}
	if missing != 0 {
		return nil, errors.New("partial replacement CRD inventory requires explicit repair before lifecycle planning")
	}
	return identities, nil
}

func validateInstallCRDInventory(identities map[string]string) error {
	if len(identities) != len(stateCRDNames) {
		return errors.New("target chart must contain the exact replacement CRD inventory")
	}
	for _, name := range stateCRDNames {
		if !validDigest(identities[name]) {
			return fmt.Errorf("target chart lacks exact CRD identity for %s", name)
		}
	}
	return nil
}

func installCRDSpecDigest(spec apiextensionsv1.CustomResourceDefinitionSpec) (string, error) {
	normalized := spec.DeepCopy()
	if normalized.Conversion != nil && normalized.Conversion.Strategy == apiextensionsv1.NoneConverter && normalized.Conversion.Webhook == nil {
		normalized.Conversion = nil
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func refusePartialInstallState(ctx context.Context, clients *Clients, namespace, release string) error {
	name := chartFullname(release)
	checks := []func() error{
		func() error {
			_, err := clients.Kubernetes.AppsV1().Deployments(namespace).Get(ctx, name+"-controller", metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := clients.Kubernetes.AppsV1().DaemonSets(namespace).Get(ctx, name+"-cni-installer", metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := clients.Kubernetes.AppsV1().DaemonSets(namespace).Get(ctx, name+"-node-agent", metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, release+"-observation-ca", metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, release+"-observation-tls", metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, "gluetun.waycloak.io", metav1.GetOptions{})
			return err
		},
	}
	for _, check := range checks {
		if err := check(); err == nil {
			return errors.New("release has runtime objects without one unambiguous deployed Helm revision; repair or uninstall before planning")
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func requiredContainer(containers []corev1.Container, name string) (corev1.Container, error) {
	for _, container := range containers {
		if container.Name == name {
			return container, nil
		}
	}
	return corev1.Container{}, fmt.Errorf("deployed release lacks %s container", name)
}

func requiredArgument(arguments []string, prefix string) (string, error) {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			value := strings.TrimPrefix(argument, prefix)
			if value != "" {
				return value, nil
			}
		}
	}
	return "", fmt.Errorf("deployed release lacks %s argument", prefix)
}

func observationCapabilityHeld(arguments []string) (bool, error) {
	held := false
	for _, argument := range arguments {
		if !strings.HasPrefix(argument, "--observation-capability-hold=") {
			continue
		}
		if argument != "--observation-capability-hold=true" || held {
			return false, errors.New("deployed node agent has an invalid observation capability hold")
		}
		held = true
	}
	return held, nil
}

func observationCapabilityHoldID(arguments []string) (string, error) {
	identity := ""
	for _, argument := range arguments {
		if !strings.HasPrefix(argument, "--observation-capability-hold-id=") {
			continue
		}
		value := strings.TrimPrefix(argument, "--observation-capability-hold-id=")
		if identity != "" || !validDigest(value) {
			return "", errors.New("deployed node agent has an invalid observation capability hold identity")
		}
		identity = value
	}
	return identity, nil
}

func validExactImageReference(reference string) bool {
	parts := strings.Split(reference, "@")
	return len(parts) == 2 && parts[0] != "" && validDigest(parts[1])
}

func validateReleaseOwnedInstallSecret(secret *corev1.Secret, release string, secretType corev1.SecretType) error {
	if secret.Annotations[installReleaseOwnerKey] != release || !validDigest(secret.Annotations[installInitialPlanKey]) {
		return fmt.Errorf("secret %s/%s is not owned by release %s", secret.Namespace, secret.Name, release)
	}
	return validateInstallSecret(secret, secretType)
}
