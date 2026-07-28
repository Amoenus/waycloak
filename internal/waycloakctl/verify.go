// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const defaultVerifyProbeURL = "https://api.ipify.org"

type VerifyReport struct {
	APIVersion         string `json:"apiVersion"`
	Kind               string `json:"kind"`
	Verified           bool   `json:"verified"`
	DistinctEgress     bool   `json:"distinctEgress"`
	ProtectedSucceeded bool   `json:"protectedSucceeded"`
	OrdinarySucceeded  bool   `json:"ordinarySucceeded"`
	TunnelLossVerified bool   `json:"tunnelLossVerified"`
	CleanupComplete    bool   `json:"cleanupComplete"`
}

func Verify(ctx context.Context, clients *Clients, namespace, gateway, image, probeURL, probeCAConfigMap, confirmation string) (report VerifyReport, err error) {
	report = VerifyReport{APIVersion: OutputAPIVersion, Kind: "VerifyReport"}
	if namespace == "" || gateway == "" || !strings.Contains(image, "@sha256:") {
		return report, errors.New("namespace, gateway, and immutable probe image are required")
	}
	parsedProbeURL, parseErr := url.Parse(probeURL)
	if parseErr != nil || parsedProbeURL.Scheme != "https" || parsedProbeURL.Host == "" || parsedProbeURL.User != nil || parsedProbeURL.Fragment != "" {
		return report, errors.New("probe URL must be an absolute HTTPS URL without user information or a fragment")
	}
	wanted := verifyConfirmation(namespace, gateway, image, probeURL, probeCAConfigMap)
	if confirmation != wanted {
		return report, fmt.Errorf("refusing smoke-test mutation: --confirm must exactly equal %s", wanted)
	}
	if _, err = clients.Kubernetes.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err != nil {
		return report, err
	}
	gatewayGVR := schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpngateways"}
	gatewayObject, err := clients.Dynamic.Resource(gatewayGVR).Namespace(namespace).Get(ctx, gateway, metav1.GetOptions{})
	if err != nil {
		return report, err
	}
	if gatewayObject.GetLabels()["verify.waycloak.io/dedicated"] != "true" {
		return report, errors.New("gateway is not explicitly labeled as dedicated for disruptive verification")
	}
	suffixBytes := make([]byte, 5)
	if _, err = rand.Read(suffixBytes); err != nil {
		return report, err
	}
	suffix := hex.EncodeToString(suffixBytes)
	prefix := "waycloak-smoke-" + suffix
	labels := map[string]string{"app.kubernetes.io/managed-by": "waycloakctl", "verify.waycloak.io/run": suffix}
	routeGVR := schema.GroupVersionResource{Group: "networking.waycloak.io", Version: "v1beta1", Resource: "vpnegressroutes"}
	route := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.waycloak.io/v1beta1", "kind": "VPNEgressRoute", "metadata": map[string]any{"name": prefix, "namespace": namespace, "labels": labels}, "spec": map[string]any{"parentRefs": []any{map[string]any{"group": "networking.waycloak.io", "kind": "VPNGateway", "namespace": namespace, "name": gateway}}, "requiredFeatures": []any{"networking.waycloak.io/TCP", "networking.waycloak.io/DNSContainment"}}}}
	createdRoute, err := clients.Dynamic.Resource(routeGVR).Namespace(namespace).Create(ctx, route, metav1.CreateOptions{})
	if err != nil {
		return report, err
	}
	ownedPods := []*corev1.Pod{}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cleanupErr := clients.Dynamic.Resource(routeGVR).Namespace(namespace).Delete(cleanupCtx, createdRoute.GetName(), metav1.DeleteOptions{})
		for _, pod := range ownedPods {
			if pod != nil {
				deleteErr := clients.Kubernetes.CoreV1().Pods(namespace).Delete(cleanupCtx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: pointer(int64(0))})
				if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
					cleanupErr = errors.Join(cleanupErr, deleteErr)
				}
			}
		}
		report.CleanupComplete = cleanupErr == nil
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err = waitRouteReady(ctx, clients, routeGVR, namespace, createdRoute.GetName(), 90*time.Second); err != nil {
		return report, err
	}
	ordinary := probePod(prefix+"-ordinary", namespace, image, probeURL, probeCAConfigMap, labels, nil)
	protected := probePod(prefix+"-protected", namespace, image, probeURL, probeCAConfigMap, labels, map[string]string{"networking.waycloak.io/egress-route": createdRoute.GetName()})
	ordinary, err = clients.Kubernetes.CoreV1().Pods(namespace).Create(ctx, ordinary, metav1.CreateOptions{})
	if err != nil {
		return report, err
	}
	ownedPods = append(ownedPods, ordinary)
	protected, err = clients.Kubernetes.CoreV1().Pods(namespace).Create(ctx, protected, metav1.CreateOptions{})
	if err != nil {
		return report, err
	}
	ownedPods = append(ownedPods, protected)
	ordinaryIP, ordinaryOK := waitProbe(ctx, clients, namespace, ordinary.Name, 2*time.Minute)
	protectedIP, protectedOK := waitProbe(ctx, clients, namespace, protected.Name, 2*time.Minute)
	report.OrdinarySucceeded, report.ProtectedSucceeded = ordinaryOK, protectedOK
	_, ordinaryErr := netip.ParseAddr(ordinaryIP)
	_, protectedErr := netip.ParseAddr(protectedIP)
	report.DistinctEgress = ordinaryErr == nil && protectedErr == nil && ordinaryIP != protectedIP
	if !report.OrdinarySucceeded || !report.ProtectedSucceeded || !report.DistinctEgress {
		return report, nil
	}
	if err = deleteExactGatewayPod(ctx, clients, namespace, gatewayObject); err != nil {
		return report, err
	}
	if err = waitGatewayReady(ctx, clients, gatewayGVR, namespace, gateway, false, 2*time.Minute); err != nil {
		return report, err
	}
	outageOrdinary := probePod(prefix+"-outage-ordinary", namespace, image, probeURL, probeCAConfigMap, labels, nil)
	outageOrdinary, err = clients.Kubernetes.CoreV1().Pods(namespace).Create(ctx, outageOrdinary, metav1.CreateOptions{})
	if err != nil {
		return report, err
	}
	ownedPods = append(ownedPods, outageOrdinary)
	_, outageOrdinaryOK := waitProbe(ctx, clients, namespace, outageOrdinary.Name, 90*time.Second)
	outageProtected := probePod(prefix+"-outage-protected", namespace, image, probeURL, probeCAConfigMap, labels, map[string]string{"networking.waycloak.io/egress-route": createdRoute.GetName()})
	outageProtected, createErr := clients.Kubernetes.CoreV1().Pods(namespace).Create(ctx, outageProtected, metav1.CreateOptions{})
	protectedDenied := false
	if createErr == nil {
		ownedPods = append(ownedPods, outageProtected)
		protectedDenied = podNeverStarted(ctx, clients, namespace, outageProtected.Name, 20*time.Second)
	} else if apierrors.IsForbidden(createErr) || apierrors.IsInvalid(createErr) {
		protectedDenied = true
	} else {
		return report, createErr
	}
	if err = waitGatewayReady(ctx, clients, gatewayGVR, namespace, gateway, true, 4*time.Minute); err != nil {
		return report, err
	}
	recovered := probePod(prefix+"-recovered", namespace, image, probeURL, probeCAConfigMap, labels, map[string]string{"networking.waycloak.io/egress-route": createdRoute.GetName()})
	recovered, err = clients.Kubernetes.CoreV1().Pods(namespace).Create(ctx, recovered, metav1.CreateOptions{})
	if err != nil {
		return report, err
	}
	ownedPods = append(ownedPods, recovered)
	recoveredIP, recoveredOK := waitProbe(ctx, clients, namespace, recovered.Name, 2*time.Minute)
	_, recoveredErr := netip.ParseAddr(recoveredIP)
	report.TunnelLossVerified = outageOrdinaryOK && protectedDenied && recoveredOK && recoveredErr == nil && recoveredIP != ordinaryIP
	report.Verified = report.OrdinarySucceeded && report.ProtectedSucceeded && report.DistinctEgress && report.TunnelLossVerified
	return report, nil
}

func deleteExactGatewayPod(ctx context.Context, clients *Clients, namespace string, gateway *unstructured.Unstructured) error {
	statefulSets, err := clients.Kubernetes.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var statefulSetUID string
	for _, statefulSet := range statefulSets.Items {
		for _, owner := range statefulSet.OwnerReferences {
			if owner.APIVersion == "networking.waycloak.io/v1beta1" && owner.Kind == "VPNGateway" && owner.Name == gateway.GetName() && string(owner.UID) == string(gateway.GetUID()) && owner.Controller != nil && *owner.Controller {
				if statefulSetUID != "" {
					return errors.New("multiple gateway StatefulSets matched exact UID")
				}
				statefulSetUID = string(statefulSet.UID)
			}
		}
	}
	if statefulSetUID == "" {
		return errors.New("exact gateway StatefulSet is unavailable")
	}
	pods, err := clients.Kubernetes.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var target *corev1.Pod
	for index := range pods.Items {
		for _, owner := range pods.Items[index].OwnerReferences {
			if owner.APIVersion == "apps/v1" && owner.Kind == "StatefulSet" && string(owner.UID) == statefulSetUID && owner.Controller != nil && *owner.Controller {
				if target != nil {
					return errors.New("multiple gateway Pods matched exact StatefulSet UID")
				}
				target = &pods.Items[index]
			}
		}
	}
	if target == nil {
		return errors.New("exact gateway Pod is unavailable")
	}
	zero := int64(0)
	return clients.Kubernetes.CoreV1().Pods(namespace).Delete(ctx, target.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
}

func waitGatewayReady(ctx context.Context, clients *Clients, gvr schema.GroupVersionResource, namespace, name string, wanted bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		item, err := clients.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
			for _, raw := range conditions {
				condition, _ := raw.(map[string]any)
				observed, _ := condition["observedGeneration"].(int64)
				if condition["type"] == "Ready" && observed == item.GetGeneration() && (condition["status"] == "True") == wanted {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("gateway Ready did not become %t", wanted)
}

func podNeverStarted(ctx context.Context, clients *Clients, namespace, name string, duration time.Duration) bool {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		pod, err := clients.Kubernetes.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			for _, status := range pod.Status.ContainerStatuses {
				started := status.Started != nil && *status.Started
				if started || status.ContainerID != "" || status.State.Running != nil || status.State.Terminated != nil {
					return false
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
	return true
}

func verifyConfirmation(namespace, gateway, image, probeURL, probeCAConfigMap string) string {
	return digestBytes([]byte(strings.Join([]string{namespace, gateway, image, probeURL, probeCAConfigMap}, "\x00")))
}

func probePod(name, namespace, image, probeURL, probeCAConfigMap string, labels, extra map[string]string) *corev1.Pod {
	all := map[string]string{}
	for k, v := range labels {
		all[k] = v
	}
	for k, v := range extra {
		all[k] = v
	}
	no := false
	probeCAFile := ""
	volumes := []corev1.Volume(nil)
	volumeMounts := []corev1.VolumeMount(nil)
	if probeCAConfigMap != "" {
		probeCAFile = "/etc/waycloak-probe-ca/ca.crt"
		volumes = []corev1.Volume{{Name: "probe-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: probeCAConfigMap}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}}}
		volumeMounts = []corev1.VolumeMount{{Name: "probe-ca", MountPath: "/etc/waycloak-probe-ca", ReadOnly: true}}
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: all}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever, Volumes: volumes, Containers: []corev1.Container{{Name: "probe", Image: image, Env: []corev1.EnvVar{{Name: "PROBE_URL", Value: probeURL}, {Name: "PROBE_CA_FILE", Value: probeCAFile}}, VolumeMounts: volumeMounts, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &no, RunAsNonRoot: pointer(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}}}}}
}

func waitRouteReady(ctx context.Context, clients *Clients, gvr schema.GroupVersionResource, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		item, err := clients.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			parents, _, _ := unstructured.NestedSlice(item.Object, "status", "parents")
			for _, raw := range parents {
				object, _ := raw.(map[string]any)
				conditions, _, _ := unstructured.NestedSlice(object, "conditions")
				ready := false
				for _, conditionRaw := range conditions {
					condition, _ := conditionRaw.(map[string]any)
					if condition["type"] == "Ready" && condition["status"] == "True" {
						ready = true
					}
				}
				if ready {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("route did not become Ready")
}

func waitProbe(ctx context.Context, clients *Clients, namespace, name string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pod, err := clients.Kubernetes.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			for _, status := range pod.Status.ContainerStatuses {
				if status.State.Terminated != nil {
					return strings.TrimSpace(status.State.Terminated.Message), status.State.Terminated.ExitCode == 0
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", false
}
func pointer[T any](value T) *T { return &value }

func runVerify(ctx context.Context, arguments []string, dependencies Dependencies) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(dependencies.Stderr)
	kubeconfig, contextName, output := clusterFlags(flags)
	namespace := flags.String("namespace", "", "gateway/workload namespace")
	gateway := flags.String("gateway", "", "dedicated smoke-test gateway")
	image := flags.String("probe-image", "", "immutable Waycloak probe image")
	probeURL := flags.String("probe-url", defaultVerifyProbeURL, "HTTPS endpoint that returns the caller's IP address")
	probeCAConfigMap := flags.String("probe-ca-configmap", "", "same-namespace ConfigMap containing public ca.crt for the probe endpoint")
	confirm := flags.String("confirm", "", "exact smoke-test identity confirmation")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
	if err != nil {
		return err
	}
	report, err := Verify(ctx, clients, *namespace, *gateway, *image, *probeURL, *probeCAConfigMap, *confirm)
	if writeErr := writeOutput(dependencies.Stdout, *output, report); err == nil {
		err = writeErr
	}
	if err == nil && !report.Verified {
		err = errors.New("smoke test did not prove distinct protected egress")
	}
	return err
}
