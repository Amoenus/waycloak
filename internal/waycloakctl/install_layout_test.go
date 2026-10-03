// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func seedTestNodeLayout(installer, agent *corev1.PodSpec, manifest ReleaseManifest) {
	installer.InitContainers[0].Args = []string{"install", "/ko-app/waycloak-cni", "/host-bin/waycloak-cni", "/host-config/10-flannel.conflist", "/host-state/install-receipt.json", "/host-config/10-flannel.conflist.waycloak-original", "/run/waycloak/cni-agent.sock", "/run/waycloak/cni-auth.key", "/var/lib/cni/waycloak/attachments", manifest.Version, manifest.ManifestDigest}
	for _, item := range []struct{ name, container, host string }{
		{"host-bin", "/host-bin", "/var/lib/rancher/k3s/data/cni"},
		{"host-config", "/host-config", "/var/lib/rancher/k3s/agent/etc/cni/net.d"},
		{"host-state", "/host-state", "/var/lib/cni/waycloak"},
	} {
		installer.Volumes = append(installer.Volumes, corev1.Volume{Name: item.name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: item.host}}})
		installer.InitContainers[0].VolumeMounts = append(installer.InitContainers[0].VolumeMounts, corev1.VolumeMount{Name: item.name, MountPath: item.container})
		agent.Volumes = append(agent.Volumes, corev1.Volume{Name: item.name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: item.host}}})
		agent.Containers[0].VolumeMounts = append(agent.Containers[0].VolumeMounts, corev1.VolumeMount{Name: item.name, MountPath: item.container, ReadOnly: true})
	}
	agent.Containers[0].Args = append(agent.Containers[0].Args, "--cni-binary-file=/host-bin/waycloak-cni", "--cni-config-file=/host-config/10-flannel.conflist", "--cni-receipt-file=/host-state/install-receipt.json")
}

func TestNodeInstallLayoutOwnedAndLegacyMounts(t *testing.T) {
	installer := corev1.PodSpec{InitContainers: []corev1.Container{{Name: "install"}}}
	agent := corev1.PodSpec{Containers: []corev1.Container{{Name: "node-agent"}}}
	manifest := ReleaseManifest{Version: "v1.0.2", ManifestDigest: "sha256:" + strings.Repeat("a", 64)}
	seedTestNodeLayout(&installer, &agent, manifest)
	installer.InitContainers[0].Args[3] = "/host-config/05-waycloak.conflist"
	installer.InitContainers[0].Args = append(installer.InitContainers[0].Args, "/host-config/10-flannel.conflist")
	agent.Containers[0].Args[1] = "--cni-config-file=/host-config/05-waycloak.conflist"
	layout, err := observeNodeInstallLayout(installer, agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(layout.ConfigPath, "/05-waycloak.conflist") || !strings.HasSuffix(layout.SourceConfigPath, "/10-flannel.conflist") {
		t.Fatalf("wrong owned layout: %+v", layout)
	}
	v, d, err := installerReleaseIdentity(installer.InitContainers[0])
	if err != nil || v != manifest.Version || d != manifest.ManifestDigest {
		t.Fatalf("optional source path corrupted release identity: %s %s %v", v, d, err)
	}
	// The same host file resolved through a legacy individual mount must retain
	// the same identity after the new chart switches to directory mounts.
	agent.Volumes[1].HostPath.Path += "/05-waycloak.conflist"
	agent.Containers[0].VolumeMounts[1].MountPath += "/05-waycloak.conflist"
	legacy, err := observeNodeInstallLayout(installer, agent)
	if err != nil || legacy.ConfigPath != layout.ConfigPath {
		t.Fatalf("legacy mount: %+v %v", legacy, err)
	}
	agent.Volumes[1].HostPath.Path = "/other/05-waycloak.conflist"
	if _, err := observeNodeInstallLayout(installer, agent); err == nil {
		t.Fatal("accepted mismatching host config")
	}
}

func TestUpgradePreservesOwnedCNIAndAllNodeCoverage(t *testing.T) {
	ctx := context.Background()
	clients := supportedClients(t)
	// Use the existing end-to-end lifecycle fixture to obtain a bound source.
	manifest := releaseManifest()
	objects, crds, _ := testInstallCRDBundle(t)
	if _, _, err := ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	seedInstalledRelease(t, clients, manifest, "waycloak-system", "waycloak", 1, objects)
	installer, _ := clients.Kubernetes.AppsV1().DaemonSets("waycloak-system").Get(ctx, "waycloak-cni-installer", metav1.GetOptions{})
	agent, _ := clients.Kubernetes.AppsV1().DaemonSets("waycloak-system").Get(ctx, "waycloak-node-agent", metav1.GetOptions{})
	installer.Spec.Template.Spec.InitContainers[0].Args[3] = "/host-config/05-waycloak.conflist"
	for i, arg := range agent.Spec.Template.Spec.Containers[0].Args {
		if strings.HasPrefix(arg, "--cni-config-file=") {
			agent.Spec.Template.Spec.Containers[0].Args[i] = "--cni-config-file=/host-config/05-waycloak.conflist"
		}
	}
	upsertTestDaemonSet(t, clients, installer)
	upsertTestDaemonSet(t, clients, agent)
	source, err := ObserveInstalledRelease(ctx, clients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Preflight(ctx, clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	target := manifest
	target.Version = "v1.0.2"
	target.ManifestDigest, err = target.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildInstallPlan(target, "waycloak-system", "waycloak", "amd64", report, source, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Values, "10-flannel.conflist") || strings.Contains(plan.Values, "kubernetes.io/arch") || strings.Count(plan.Values, "05-waycloak.conflist") != 2 || strings.Count(plan.Values, "nodeSelector: {}") != 2 {
		t.Fatalf("upgrade changed host layout or node coverage:\n%s", plan.Values)
	}
	if _, err := observeInstallTransitionCheckpoint(ctx, clients, plan, crds); err != nil {
		t.Fatal(err)
	}
	// A scheduling change after review must be rejected before a journal or
	// Helm mutation, even when all image and release identities still match.
	agent.Spec.Template.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": "amd64"}
	upsertTestDaemonSet(t, clients, agent)
	if _, err := observeInstallTransitionCheckpoint(ctx, clients, plan, crds); err == nil {
		t.Fatal("accepted node coverage drift after plan review")
	}
}
