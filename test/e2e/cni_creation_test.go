// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycni "github.com/Amoenus/waycloak/internal/cni"
	waycontroller "github.com/Amoenus/waycloak/internal/controller"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	cniRouteLabel       = "networking.waycloak.io/egress-route"
	cniProbeDestination = "198.51.100.123"
)

func TestChainedCNICreationTimeFailClosed(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_CNI") != "1" {
		t.Skip("set WAYCLOAK_E2E_CNI=1 on a disposable or explicitly authorized node")
	}
	recovery, err := parseDatastoreRecoveryConfig(
		os.Getenv("WAYCLOAK_E2E_CNI_SNAPSHOT_COMMAND"),
		os.Getenv("WAYCLOAK_E2E_CNI_RESTORE_COMMAND"),
	)
	must(t, err)
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	nodeName := strings.TrimSpace(os.Getenv("WAYCLOAK_E2E_CNI_NODE"))
	if nodeName == "" && !strings.HasPrefix(contextName, "kind-") {
		t.Fatal("WAYCLOAK_E2E_CNI_NODE must explicitly select a non-Kind node")
	}

	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, rbacv1.AddToScheme(scheme))
	must(t, wayv1.AddToScheme(scheme))
	direct, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	ctx := context.Background()
	if nodeName == "" {
		var nodes corev1.NodeList
		must(t, direct.List(ctx, &nodes))
		if len(nodes.Items) != 1 {
			t.Fatalf("disposable Kind proof requires exactly one node, found %d", len(nodes.Items))
		}
		nodeName = nodes.Items[0].Name
	}
	var node corev1.Node
	must(t, direct.Get(ctx, client.ObjectKey{Name: nodeName}, &node))
	architecture := node.Status.NodeInfo.Architecture
	if architecture != "amd64" && architecture != "arm64" {
		t.Fatalf("unsupported node architecture %q", architecture)
	}

	cniConfigDirectory := strings.TrimSpace(os.Getenv("WAYCLOAK_E2E_CNI_CONFIG_DIR"))
	if cniConfigDirectory == "" {
		cniConfigDirectory = "/etc/cni/net.d"
	}
	cniBinaryDirectory := strings.TrimSpace(os.Getenv("WAYCLOAK_E2E_CNI_BIN_DIR"))
	if cniBinaryDirectory == "" {
		cniBinaryDirectory = "/opt/cni/bin"
	}
	cniConfigName := strings.TrimSpace(os.Getenv("WAYCLOAK_E2E_CNI_CONFIG_NAME"))
	if cniConfigName == "" {
		cniConfigName = "10-kindnet.conflist"
	}
	if strings.Contains(cniConfigName, "/") || strings.Contains(cniConfigName, "\\") {
		t.Fatalf("CNI configuration name must be one exact basename: %q", cniConfigName)
	}

	cniBinary := filepath.Join(t.TempDir(), "waycloak-cni")
	agentBinary := filepath.Join(t.TempDir(), "waycloak-cni-agent")
	buildLinuxBinary(t, architecture, cniBinary, "../../cmd/waycloak-cni")
	buildLinuxBinary(t, architecture, agentBinary, "../../test/fixtures/cni-agent")

	namespace := fmt.Sprintf("waycloak-cni-e2e-%d", time.Now().UnixNano())
	roleName := namespace
	must(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}))
	must(t, direct.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: namespace}}))
	must(t, direct.Create(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}}))
	must(t, direct.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "agent", Namespace: namespace}}}))
	t.Cleanup(func() {
		_ = direct.Delete(context.Background(), &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}})
		_ = direct.Delete(context.Background(), &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}})
		_ = direct.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	agentPod := cniHostPod("agent", namespace, nodeName, "agent", []corev1.Volume{
		hostDirectory("run", "/run/waycloak-cni-e2e", true), hostDirectory("netns", "/var/run/netns", false),
	}, []corev1.VolumeMount{{Name: "run", MountPath: "/host-run"}, {Name: "netns", MountPath: "/var/run/netns", ReadOnly: true}})
	installerPod := cniHostPod("installer", namespace, nodeName, "", []corev1.Volume{
		hostDirectory("bin", cniBinaryDirectory, false), hostDirectory("config", cniConfigDirectory, false), hostDirectory("state", "/var/lib/cni", false), hostDirectory("run", "/run/waycloak-cni-e2e", true), hostDirectory("netns", "/var/run/netns", false),
	}, []corev1.VolumeMount{{Name: "bin", MountPath: "/host-bin"}, {Name: "config", MountPath: "/host-config"}, {Name: "state", MountPath: "/host-state"}, {Name: "run", MountPath: "/host-run"}, {Name: "netns", MountPath: "/var/run/netns"}})
	must(t, direct.Create(ctx, agentPod))
	must(t, direct.Create(ctx, installerPod))
	waitForPodReady(t, direct, agentPod)
	waitForPodReady(t, direct, installerPod)
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "exec", "-n", namespace, installerPod.Name, "--", "sh", "-c", "find /host-run -mindepth 1 -maxdepth 1 -delete").Run()
	})
	copyLocalFile(t, agentBinary, namespace, agentPod.Name, "/tmp/waycloak-cni-agent")
	command(t, nil, "kubectl", "exec", "-n", namespace, agentPod.Name, "--", "chmod", "+x", "/tmp/waycloak-cni-agent")
	startFixtureAgent(t, namespace, agentPod.Name)
	waitFor(t, 20*time.Second, func() bool { return commandSucceeds(namespace, installerPod.Name, "test -S /host-run/agent.sock") })

	original := filepath.Join(t.TempDir(), cniConfigName+".original")
	copyFromPod(t, namespace, installerPod.Name, "/host-config/"+cniConfigName, original)
	rendered := filepath.Join(t.TempDir(), cniConfigName)
	appendWaycloakPlugin(t, original, rendered)
	copyLocalFile(t, cniBinary, namespace, installerPod.Name, "/tmp/waycloak-cni")
	copyLocalFile(t, rendered, namespace, installerPod.Name, "/tmp/waycloak.conflist")

	installed := false
	t.Cleanup(func() {
		if installed {
			bestEffortCopyToPod(original, namespace, installerPod.Name, "/tmp/original.conflist")
			_ = exec.Command("kubectl", "exec", "-n", namespace, installerPod.Name, "--", "install", "-m", "0644", "/tmp/original.conflist", "/host-config/"+cniConfigName).Run()
			_ = exec.Command("kubectl", "exec", "-n", namespace, installerPod.Name, "--", "rm", "-f", "/host-bin/waycloak-cni").Run()
			_ = exec.Command("kubectl", "exec", "-n", namespace, installerPod.Name, "--", "rm", "-rf", "/host-state/waycloak-e2e").Run()
		}
	})
	command(t, nil, "kubectl", "exec", "-n", namespace, installerPod.Name, "--", "install", "-m", "0755", "/tmp/waycloak-cni", "/host-bin/waycloak-cni")
	command(t, nil, "kubectl", "exec", "-n", namespace, installerPod.Name, "--", "install", "-m", "0644", "/tmp/waycloak.conflist", "/host-config/"+cniConfigName)
	installed = true
	assertLocalProtocolAuthentication(t, namespace, installerPod.Name)

	before := readCaptureCounts(t, namespace, agentPod.Name)
	control := cniTrafficPod("ordinary-control", namespace, nodeName, nil)
	must(t, direct.Create(ctx, control))
	waitForPodPhase(t, direct, control, corev1.PodSucceeded, 90*time.Second)
	afterControl := readCaptureCounts(t, namespace, agentPod.Name)
	if !afterControl.allIncreasedFrom(before) {
		t.Fatalf("packet capture positive control did not observe every direct probe class: before=%#v after=%#v", before, afterControl)
	}

	// This privileged proof deliberately installs no admission policy and
	// direct-assigns the enrolled Pod to a Node. It is the stale/bypassed
	// admission case: chained CNI must still prevent a runnable sandbox and all
	// application packets without relying on the scheduling label.
	failing := cniTrafficPod("protected-missing-binding", namespace, nodeName, map[string]string{cniRouteLabel: "missing"})
	must(t, direct.Create(ctx, failing))
	waitFor(t, 20*time.Second, func() bool {
		return commandSucceeds(namespace, installerPod.Name, "test -n \"$(find /host-state/waycloak-e2e -type f -name '*.json' -print -quit 2>/dev/null)\"")
	})
	attachment, statePath, stateData := readRemoteAttachment(t, namespace, installerPod.Name)
	if output, err := execCNI(namespace, installerPod.Name, "CHECK", attachment, attachment.Pod.NetNS, nil); err == nil {
		t.Fatalf("CHECK did not fail closed for a locked-down attachment: err=%v output=%s", err, output)
	}
	var relabeled corev1.Pod
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(failing), &relabeled))
	if recovery.SnapshotCommand == "" {
		beforeRelabel := relabeled.DeepCopy()
		delete(relabeled.Labels, cniRouteLabel)
		must(t, direct.Patch(ctx, &relabeled, client.MergeFrom(beforeRelabel)))
	}
	restartFixtureAgent(t, namespace, agentPod.Name)
	if restartCommand := strings.TrimSpace(os.Getenv("WAYCLOAK_E2E_CNI_RESTART_RUNTIME_COMMAND")); restartCommand != "" {
		runHostCommand(t, restartCommand)
		waitForNodeReady(t, direct, nodeName, 2*time.Minute)
		waitForPodReady(t, direct, agentPod)
		waitForPodReady(t, direct, installerPod)
		if !commandSucceeds(namespace, installerPod.Name, "test -S /host-run/agent.sock") {
			startFixtureAgent(t, namespace, agentPod.Name)
		}
		waitFor(t, 20*time.Second, func() bool { return commandSucceeds(namespace, installerPod.Name, "test -S /host-run/agent.sock") })
	}
	waitForSandboxFailure(t, direct, failing, 60*time.Second)
	time.Sleep(500 * time.Millisecond)
	afterFailure := readCaptureCounts(t, namespace, agentPod.Name)
	if afterFailure != afterControl {
		t.Fatalf("captured direct packets during denied ADD: baseline=%#v after=%#v", afterControl, afterFailure)
	}
	var observed corev1.Pod
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(failing), &observed))
	if len(observed.Status.InitContainerStatuses) != 0 {
		t.Fatalf("application container started after failed ADD: containers=%#v init=%#v", observed.Status.ContainerStatuses, observed.Status.InitContainerStatuses)
	}
	for _, status := range observed.Status.ContainerStatuses {
		started := status.Started != nil && *status.Started
		if started || status.ContainerID != "" || status.State.Running != nil || status.State.Terminated != nil || status.LastTerminationState.Running != nil || status.LastTerminationState.Terminated != nil {
			t.Fatalf("application container started after failed ADD: %#v", status)
		}
	}
	if len(observed.Spec.Containers) != 1 || len(observed.Spec.InitContainers) != 0 || observed.Spec.Containers[0].SecurityContext != nil {
		t.Fatalf("CNI path mutated or privileged the application Pod: %#v", observed.Spec)
	}
	if recovery.SnapshotCommand != "" {
		direct = runDatastoreRecoveryProof(t, scheme, direct, recovery, namespace, nodeName, failing, agentPod, installerPod, agentBinary, attachment, statePath, stateData, afterControl)
	}
	must(t, direct.Delete(ctx, failing, client.GracePeriodSeconds(0)))
	runtimeCleaned := conditionWithin(10*time.Second, func() bool {
		return commandSucceeds(namespace, installerPod.Name, "test -z \"$(find /host-state/waycloak-e2e -type f -name '*.json' -print -quit 2>/dev/null)\"")
	})
	if runtimeCleaned {
		staleLocal := filepath.Join(t.TempDir(), path.Base(statePath))
		must(t, os.WriteFile(staleLocal, stateData, 0o600))
		copyLocalFile(t, staleLocal, namespace, installerPod.Name, "/tmp/stale-attachment.json")
		command(t, nil, "kubectl", "exec", "-n", namespace, installerPod.Name, "--", "install", "-m", "0600", "/tmp/stale-attachment.json", "/host-state/waycloak-e2e/"+path.Base(statePath))
	}
	if output, err := execCNI(namespace, installerPod.Name, "GC", attachment, "", map[string]any{"validAttachments": []any{}}); err != nil {
		t.Fatalf("GC failed to remove stale missing-netns attachment: %v: %s", err, output)
	}
	waitFor(t, 10*time.Second, func() bool {
		return commandSucceeds(namespace, installerPod.Name, "test -z \"$(find /host-state/waycloak-e2e -type f -name '*.json' -print -quit 2>/dev/null)\"")
	})
	for i := 0; i < 2; i++ {
		if output, err := execCNI(namespace, installerPod.Name, "DEL", attachment, "", nil); err != nil {
			t.Fatalf("idempotent DEL %d failed with missing netns: %v: %s", i+1, err, output)
		}
	}

	secondControl := cniTrafficPod("ordinary-after-failure", namespace, nodeName, nil)
	must(t, direct.Create(ctx, secondControl))
	waitForPodPhase(t, direct, secondControl, corev1.PodSucceeded, 90*time.Second)
	if final := readCaptureCounts(t, namespace, agentPod.Name); !final.allIncreasedFrom(afterFailure) {
		t.Fatalf("primary CNI did not restore every direct probe class after Waycloak failure and DEL: before=%#v after=%#v", afterFailure, final)
	}
}

func runDatastoreRecoveryProof(
	t *testing.T,
	scheme *runtime.Scheme,
	direct client.Client,
	recovery datastoreRecoveryConfig,
	namespace, nodeName string,
	failing, agentPod, installerPod *corev1.Pod,
	agentBinary string,
	attachment waycni.Attachment,
	statePath string,
	stateData []byte,
	baseline captureCounts,
) client.Client {
	t.Helper()
	ctx := context.Background()
	var currentNamespace corev1.Namespace
	must(t, direct.Get(ctx, client.ObjectKey{Name: namespace}, &currentNamespace))
	var currentPod corev1.Pod
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(failing), &currentPod))
	observedAt := time.Now().UTC().Truncate(time.Second)
	binding := &wayv1.VPNWorkloadBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNWorkloadBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: "snapshot-binding", Namespace: namespace},
		Spec: wayv1.VPNWorkloadBindingSpec{
			PodRef:     wayv1.LocalUIDReference{Name: wayv1.ObjectName(currentPod.Name), UID: wayv1.ObjectUID(currentPod.UID)},
			RouteRef:   wayv1.LocalUIDReference{Name: "snapshot-route", UID: "00000000-0000-4000-8000-000000000001"},
			GatewayRef: wayv1.NamespacedUIDReference{Namespace: wayv1.NamespaceName(namespace), Name: "snapshot-gateway", UID: "00000000-0000-4000-8000-000000000002"},
			NodeName:   wayv1.ObjectName(nodeName),
			Allocation: wayv1.WorkloadAllocation{Identity: "snapshot-allocation", Address: "100.96.0.2/32"},
			Network: wayv1.WorkloadNetworkIntent{
				GatewayGeneration: 1, OverlayCIDR: "100.96.0.0/24", GatewayAddress: "100.96.0.1",
				GatewayEndpoint: "192.0.2.1:51820", GatewayHealthPort: 51821, VNI: 1042, MTU: 1380,
				ClusterTraffic: wayv1.ClusterTraffic{Mode: wayv1.ClusterTrafficTunnelAll},
			},
		},
	}
	must(t, direct.Create(ctx, binding))
	binding.Status = wayv1.VPNWorkloadBindingStatus{
		ObservedGeneration: binding.Generation,
		AppliedGeneration:  binding.Generation,
		ObservedPodUID:     binding.Spec.PodRef.UID,
		ObservedGatewayUID: binding.Spec.GatewayRef.UID,
		Agent: &wayv1.NodeAgentObservation{
			NodeName: binding.Spec.NodeName, NodeBootID: "snapshot-node-boot", InstanceID: "snapshot-agent",
			ObservedAt: metav1.NewTime(observedAt),
		},
		Conditions: wayv1.BindingConditions{
			freshCondition(wayv1.ConditionAccepted, metav1.ConditionTrue, wayv1.ReasonAccepted, binding.Generation, observedAt),
			freshCondition(wayv1.ConditionResolvedRefs, metav1.ConditionTrue, wayv1.ReasonResolvedRefs, binding.Generation, observedAt),
			freshCondition(wayv1.ConditionProgrammed, metav1.ConditionTrue, wayv1.ReasonProgrammed, binding.Generation, observedAt),
			freshCondition(wayv1.ConditionReady, metav1.ConditionTrue, wayv1.ReasonReady, binding.Generation, observedAt),
			freshCondition(wayv1.ConditionNodeReady, metav1.ConditionTrue, wayv1.ReasonNodeReady, binding.Generation, observedAt),
		},
	}
	statusApply := &wayv1.VPNWorkloadBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: wayv1.GroupVersion.String(), Kind: "VPNWorkloadBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: binding.Name, Namespace: binding.Namespace},
		Status:     binding.Status,
	}
	statusData, marshalErr := json.Marshal(statusApply)
	must(t, marshalErr)
	must(t, direct.SubResource("status").Patch(ctx, statusApply, client.RawPatch(types.ApplyPatchType, statusData), client.FieldOwner(wayv1.FieldManagerBindingController)))
	expected := restoredIdentities{
		NamespaceUID: string(currentNamespace.UID), PodUID: string(currentPod.UID), BindingUID: string(binding.UID),
	}

	runHostCommand(t, recovery.SnapshotCommand)
	marker := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "post-snapshot-marker", Namespace: namespace}}
	must(t, direct.Create(ctx, marker))
	var changed wayv1.VPNWorkloadBinding
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(binding), &changed))
	changed.Annotations = map[string]string{"networking.waycloak.io/post-snapshot": "must-disappear"}
	must(t, direct.Update(ctx, &changed))
	runHostCommand(t, recovery.RestoreCommand)

	var err error
	direct, err = client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	must(t, err)
	waitForNodeReady(t, direct, nodeName, 3*time.Minute)
	waitForPodReady(t, direct, agentPod)
	waitForPodReady(t, direct, installerPod)

	var restoredNamespace corev1.Namespace
	must(t, direct.Get(ctx, client.ObjectKey{Name: namespace}, &restoredNamespace))
	var restoredPod corev1.Pod
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(failing), &restoredPod))
	var restoredBinding wayv1.VPNWorkloadBinding
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(binding), &restoredBinding))
	var restoredMarker corev1.ConfigMap
	markerErr := direct.Get(ctx, client.ObjectKeyFromObject(marker), &restoredMarker)
	if markerErr != nil && !apierrors.IsNotFound(markerErr) {
		t.Fatalf("observe post-snapshot marker after restore: %v", markerErr)
	}
	actual := restoredIdentities{
		NamespaceUID: string(restoredNamespace.UID), PodUID: string(restoredPod.UID), BindingUID: string(restoredBinding.UID),
	}
	if err := validateRestoredIdentities(expected, actual, markerErr == nil); err != nil {
		t.Fatal(err)
	}
	if restoredPod.Labels[cniRouteLabel] != "missing" {
		t.Fatalf("restored Pod lost exact enrollment label: %#v", restoredPod.Labels)
	}
	if restoredBinding.Annotations["networking.waycloak.io/post-snapshot"] != "" {
		t.Fatal("post-snapshot binding mutation survived datastore restore")
	}
	if condition := meta.FindStatusCondition(restoredBinding.Status.Conditions, wayv1.ConditionReady); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("snapshot did not restore the deliberately live-looking Ready condition: %#v", condition)
	}

	reconciler := &waycontroller.VPNWorkloadBindingReconciler{
		Client: direct, APIReader: direct, ObservationTTL: 30 * time.Second,
		Now: func() time.Time { return observedAt.Add(2 * time.Minute) },
	}
	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&restoredBinding)})
	must(t, err)
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(&restoredBinding), &restoredBinding))
	for conditionType, reason := range map[string]string{
		wayv1.ConditionReady: wayv1.ReasonNotReady, wayv1.ConditionNodeReady: wayv1.ReasonNodeNotReady,
	} {
		condition := meta.FindStatusCondition(restoredBinding.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != reason || condition.ObservedGeneration != restoredBinding.Generation {
			t.Fatalf("restored stale %s condition was not withdrawn: %#v", conditionType, condition)
		}
	}

	copyLocalFile(t, agentBinary, namespace, agentPod.Name, "/tmp/waycloak-cni-agent")
	command(t, nil, "kubectl", "exec", "-n", namespace, agentPod.Name, "--", "sh", "-c", "if test -f /host-run/agent.pid; then kill $(cat /host-run/agent.pid) 2>/dev/null || true; fi; rm -f /host-run/agent.sock /host-run/agent.pid")
	startFixtureAgent(t, namespace, agentPod.Name)
	waitFor(t, 20*time.Second, func() bool { return commandSucceeds(namespace, installerPod.Name, "test -S /host-run/agent.sock") })
	if output, checkErr := execCNI(namespace, installerPod.Name, "CHECK", attachment, attachment.Pod.NetNS, nil); checkErr == nil {
		t.Fatalf("CHECK stopped failing closed after coherent datastore restore: %s", output)
	}
	_, restoredStatePath, restoredState := readRemoteAttachment(t, namespace, installerPod.Name)
	if path.Base(restoredStatePath) != path.Base(statePath) || !json.Valid(restoredState) || !json.Valid(stateData) {
		t.Fatalf("durable CNI attachment identity was lost across restore: before=%q after=%q", statePath, restoredStatePath)
	}
	waitForSandboxFailure(t, direct, &restoredPod, 60*time.Second)
	must(t, direct.Get(ctx, client.ObjectKeyFromObject(&restoredPod), &restoredPod))
	assertApplicationNeverStarted(t, &restoredPod)
	time.Sleep(500 * time.Millisecond)
	if after := readCaptureCounts(t, namespace, agentPod.Name); after != baseline {
		t.Fatalf("captured direct packets during datastore recovery: baseline=%#v after=%#v", baseline, after)
	}
	return direct
}

func freshCondition(conditionType string, status metav1.ConditionStatus, reason string, generation int64, now time.Time) metav1.Condition {
	return metav1.Condition{
		Type: conditionType, Status: status, Reason: reason, Message: reason,
		ObservedGeneration: generation, LastTransitionTime: metav1.NewTime(now),
	}
}

func assertApplicationNeverStarted(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	if len(pod.Status.InitContainerStatuses) != 0 {
		t.Fatalf("application init container started after failed ADD: %#v", pod.Status.InitContainerStatuses)
	}
	for _, status := range pod.Status.ContainerStatuses {
		started := status.Started != nil && *status.Started
		if started || status.ContainerID != "" || status.State.Running != nil || status.State.Terminated != nil || status.LastTerminationState.Running != nil || status.LastTerminationState.Terminated != nil {
			t.Fatalf("application container started after failed ADD: %#v", status)
		}
	}
}

func conditionWithin(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return condition()
}

func readRemoteAttachment(t *testing.T, namespace, pod string) (waycni.Attachment, string, []byte) {
	t.Helper()
	statePath := strings.TrimSpace(command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "find", "/host-state/waycloak-e2e", "-type", "f", "-name", "*.json", "-print", "-quit"))
	if statePath == "" {
		t.Fatal("CNI attachment state path is empty")
	}
	data := []byte(command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "cat", statePath))
	var attachment waycni.Attachment
	must(t, json.Unmarshal(data, &attachment))
	return attachment, statePath, data
}

func execCNI(namespace, pod, cniCommand string, attachment waycni.Attachment, netns string, extra map[string]any) ([]byte, error) {
	configuration := map[string]any{
		"cniVersion": "1.1.0", "name": attachment.Network, "type": "waycloak-cni",
		"agentSocket": "/host-run/agent.sock", "agentKeyFile": "/host-run/agent.key", "stateDir": "/host-state/waycloak-e2e",
	}
	if cniCommand == "CHECK" {
		configuration["prevResult"] = map[string]any{"cniVersion": "1.1.0", "interfaces": []any{map[string]any{"name": attachment.Pod.IfName, "sandbox": netns}}}
	}
	for key, value := range extra {
		configuration[key] = value
	}
	stdin, err := json.Marshal(configuration)
	if err != nil {
		return nil, err
	}
	arguments := strings.Join([]string{
		"IgnoreUnknown=1", "K8S_POD_NAMESPACE=" + attachment.Pod.Namespace, "K8S_POD_NAME=" + attachment.Pod.Name,
		"K8S_POD_UID=" + attachment.Pod.UID, "K8S_POD_INFRA_CONTAINER_ID=" + attachment.Pod.ContainerID,
	}, ";")
	invocation := exec.Command("kubectl", "exec", "-i", "-n", namespace, pod, "--", "env",
		"CNI_COMMAND="+cniCommand, "CNI_CONTAINERID="+attachment.Pod.ContainerID, "CNI_NETNS="+netns,
		"CNI_IFNAME="+attachment.Pod.IfName, "CNI_PATH=/host-bin", "CNI_ARGS="+arguments, "/host-bin/waycloak-cni")
	invocation.Stdin = strings.NewReader(string(stdin))
	return invocation.CombinedOutput()
}

func assertLocalProtocolAuthentication(t *testing.T, namespace, pod string) {
	t.Helper()
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "dd", "if=/dev/zero", "of=/host-run/foreign.key", "bs=32", "count=1", "status=none")
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "chmod", "0600", "/host-run/foreign.key")
	if output, err := execCNIStatus(namespace, pod, "/host-run/foreign.key"); err == nil {
		t.Fatalf("CNI STATUS authenticated with a foreign key: %s", output)
	}
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "chmod", "0644", "/host-run/agent.key")
	if output, err := execCNIStatus(namespace, pod, "/host-run/agent.key"); err == nil {
		t.Fatalf("CNI STATUS accepted an over-permissive key file: %s", output)
	}
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "chmod", "0600", "/host-run/agent.key")
	if output, err := execCNIStatus(namespace, pod, "/host-run/agent.key"); err != nil {
		t.Fatalf("CNI STATUS rejected the root-only current key: %v: %s", err, output)
	}
}

func execCNIStatus(namespace, pod, keyFile string) ([]byte, error) {
	configuration := map[string]any{
		"cniVersion": "1.1.0", "name": "waycloak-e2e", "type": "waycloak-cni",
		"agentSocket": "/host-run/agent.sock", "agentKeyFile": keyFile, "stateDir": "/host-state/waycloak-e2e",
	}
	stdin, err := json.Marshal(configuration)
	if err != nil {
		return nil, err
	}
	invocation := exec.Command("kubectl", "exec", "-i", "-n", namespace, pod, "--", "env", "CNI_COMMAND=STATUS", "CNI_PATH=/host-bin", "/host-bin/waycloak-cni")
	invocation.Stdin = strings.NewReader(string(stdin))
	return invocation.CombinedOutput()
}

func runHostCommand(t *testing.T, commandLine string) {
	t.Helper()
	arguments := strings.Fields(commandLine)
	if len(arguments) == 0 {
		t.Fatal("runtime restart command is empty")
	}
	invocation := exec.Command(arguments[0], arguments[1:]...)
	if output, err := invocation.CombinedOutput(); err != nil {
		t.Fatalf("runtime restart command failed: %v: %s", err, output)
	}
}

func waitForNodeReady(t *testing.T, direct client.Client, nodeName string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() bool {
		var node corev1.Node
		if direct.Get(context.Background(), client.ObjectKey{Name: nodeName}, &node) != nil {
			return false
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				return condition.Status == corev1.ConditionTrue
			}
		}
		return false
	})
}

func buildLinuxBinary(t *testing.T, architecture, output, pkg string) {
	t.Helper()
	command(t, append(os.Environ(), "GOOS=linux", "GOARCH="+architecture, "CGO_ENABLED=0"), "go", "build", "-trimpath", "-o", output, pkg)
}

func hostDirectory(name, path string, create bool) corev1.Volume {
	typeValue := corev1.HostPathDirectory
	if create {
		typeValue = corev1.HostPathDirectoryOrCreate
	}
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path, Type: &typeValue}}}
}

func cniHostPod(name, namespace, node, serviceAccount string, volumes []corev1.Volume, mounts []corev1.VolumeMount) *corev1.Pod {
	yes := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: corev1.PodSpec{
		NodeName: node, HostNetwork: true, AutomountServiceAccountToken: &yes, RestartPolicy: corev1.RestartPolicyNever,
		Volumes: volumes, Containers: []corev1.Container{{Name: name, Image: "alpine:3.22.1", Command: []string{"sleep", "3600"}, VolumeMounts: mounts, SecurityContext: &corev1.SecurityContext{Privileged: &yes}}},
	}}
	if serviceAccount != "" {
		pod.Spec.ServiceAccountName = serviceAccount
	}
	return pod
}

func cniTrafficPod(name, namespace, node string, labels map[string]string) *corev1.Pod {
	no := false
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Spec: corev1.PodSpec{
		NodeName: node, AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{Name: "application", Image: "alpine:3.22.1", Command: []string{"sh", "-ec", "for i in 1 2 3; do timeout 2 sh -c 'printf probe | nc -u " + cniProbeDestination + " 18081' || true; timeout 2 nc -z " + cniProbeDestination + " 18080 || true; timeout 2 sh -c 'printf dns | nc -u " + cniProbeDestination + " 53' || true; timeout 2 nc -z " + cniProbeDestination + " 53 || true; timeout 2 sh -c 'head -c 4096 /dev/zero | nc -u " + cniProbeDestination + " 18082' || true; done"}}},
	}}
}

func startFixtureAgent(t *testing.T, namespace, pod string) {
	t.Helper()
	commandLine := "nohup /tmp/waycloak-cni-agent --socket=/host-run/agent.sock --auth-key-file=/host-run/agent.key --capture-file=/host-run/capture-count >/host-run/agent.log 2>&1 </dev/null & echo $! >/host-run/agent.pid"
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "sh", "-c", commandLine)
}

func restartFixtureAgent(t *testing.T, namespace, pod string) {
	t.Helper()
	command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "sh", "-c", "kill $(cat /host-run/agent.pid); for i in $(seq 1 50); do test ! -S /host-run/agent.sock && break; sleep .02; done")
	startFixtureAgent(t, namespace, pod)
}

func appendWaycloakPlugin(t *testing.T, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	must(t, err)
	var conflist struct {
		CNIVersion string                   `json:"cniVersion"`
		Name       string                   `json:"name"`
		Plugins    []map[string]interface{} `json:"plugins"`
	}
	must(t, json.Unmarshal(data, &conflist))
	if conflist.CNIVersion == "" || conflist.Name == "" || len(conflist.Plugins) == 0 {
		t.Fatalf("primary CNI conflist is incomplete")
	}
	for _, plugin := range conflist.Plugins {
		if plugin["type"] == "waycloak-cni" {
			t.Fatalf("refusing to modify a CNI conflist that already contains Waycloak")
		}
	}
	conflist.Plugins = append(conflist.Plugins, map[string]interface{}{
		"type": "waycloak-cni", "agentSocket": "/run/waycloak-cni-e2e/agent.sock", "agentKeyFile": "/run/waycloak-cni-e2e/agent.key", "stateDir": "/var/lib/cni/waycloak-e2e",
		"resolveTimeout": "2s", "bindingTimeout": "5s", "retryInterval": "100ms",
	})
	rendered, err := json.MarshalIndent(conflist, "", "  ")
	must(t, err)
	must(t, os.WriteFile(target, append(rendered, '\n'), 0o600))
}

func copyFromPod(t *testing.T, namespace, pod, source, target string) {
	t.Helper()
	contents, err := exec.Command("kubectl", "exec", "-n", namespace, pod, "--", "cat", source).Output()
	must(t, err)
	must(t, os.WriteFile(target, contents, 0o600))
}

func bestEffortCopyToPod(source, namespace, pod, target string) {
	contents, err := os.Open(source)
	if err != nil {
		return
	}
	defer contents.Close()
	copyCommand := exec.Command("kubectl", "exec", "-i", "-n", namespace, pod, "--", "sh", "-c", "cat > \"$1\"", "sh", target)
	copyCommand.Stdin = contents
	_ = copyCommand.Run()
}

type captureCounts struct {
	TCP      uint64 `json:"tcp"`
	UDP      uint64 `json:"udp"`
	DNSUDP   uint64 `json:"dnsUDP"`
	DNSTCP   uint64 `json:"dnsTCP"`
	Fragment uint64 `json:"fragment"`
}

func (c captureCounts) allIncreasedFrom(previous captureCounts) bool {
	return c.TCP > previous.TCP && c.UDP > previous.UDP && c.DNSUDP > previous.DNSUDP && c.DNSTCP > previous.DNSTCP && c.Fragment > previous.Fragment
}

func readCaptureCounts(t *testing.T, namespace, pod string) captureCounts {
	t.Helper()
	value := strings.TrimSpace(command(t, nil, "kubectl", "exec", "-n", namespace, pod, "--", "cat", "/host-run/capture-count"))
	var parsed captureCounts
	must(t, json.Unmarshal([]byte(value), &parsed))
	return parsed
}

func waitForPodPhase(t *testing.T, direct client.Client, pod *corev1.Pod, phase corev1.PodPhase, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var current corev1.Pod
	for time.Now().Before(deadline) {
		if direct.Get(context.Background(), client.ObjectKeyFromObject(pod), &current) == nil && current.Status.Phase == phase {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err := direct.Get(context.Background(), client.ObjectKeyFromObject(pod), &current); err != nil {
		t.Fatalf("Pod did not reach phase %s and final observation failed: %v", phase, err)
	}
	var events corev1.EventList
	_ = direct.List(context.Background(), &events, client.InNamespace(pod.Namespace))
	diagnostics := make([]string, 0, len(events.Items))
	for _, event := range events.Items {
		if event.InvolvedObject.UID == current.UID {
			diagnostics = append(diagnostics, event.Reason+": "+event.Message)
		}
	}
	t.Fatalf("Pod did not reach phase %s: phase=%s reason=%q message=%q containers=%#v events=%q", phase, current.Status.Phase, current.Status.Reason, current.Status.Message, current.Status.ContainerStatuses, diagnostics)
}

func waitForSandboxFailure(t *testing.T, direct client.Client, pod *corev1.Pod, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() bool {
		var events corev1.EventList
		if direct.List(context.Background(), &events, client.InNamespace(pod.Namespace)) != nil {
			return false
		}
		for _, event := range events.Items {
			if event.InvolvedObject.Name == pod.Name && event.Reason == "FailedCreatePodSandBox" && strings.Contains(strings.ToLower(event.Message), "waycloak") {
				return true
			}
		}
		return false
	})
}
