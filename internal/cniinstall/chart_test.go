// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cniinstall

import (
	"io"
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestChartObservesHostDirectoriesAndInstallsOwnedConfig(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm is required for chart contract verification")
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	args := []string{"template", "waycloak", "../../charts/waycloak", "--show-only", "templates/node-agent.yaml", "--show-only", "templates/cni-installer.yaml"}
	for _, setting := range []string{
		"controller.enabled=true", "controller.image.digest=" + digest,
		"controller.gateway.engineImage.digest=" + digest, "controller.gateway.agentImage.digest=" + digest,
		"controller.gateway.coreDNSImage.digest=" + digest, "controller.gateway.overlayCIDR=100.96.0.0/16",
		"controller.gateway.clusterDNS.serviceIP=10.96.0.10", "controller.gateway.clusterDNS.domain=cluster.local",
		"releaseIdentity.version=v0.0.0-test", "releaseIdentity.manifestDigest=" + digest,
		"nodeAgent.enabled=true", "nodeAgent.image.digest=" + digest,
		"nodeAgent.cniReceiptHostPath=/var/lib/cni/waycloak/custom-receipt.json",
		"nodeAgent.cniBinaryHostPath=/opt/cni/bin/waycloak-cni",
		"nodeAgent.cniConfigHostPath=/etc/cni/net.d/05-waycloak.conflist",
		"cniInstaller.enabled=true", "cniInstaller.image.digest=" + digest, "cniInstaller.pauseImage.digest=" + digest,
		"cniInstaller.receiptHostPath=/var/lib/cni/waycloak/custom-receipt.json",
		"cniInstaller.binaryHostPath=/opt/cni/bin/waycloak-cni",
		"cniInstaller.configHostPath=/etc/cni/net.d/05-waycloak.conflist",
		"cniInstaller.sourceConfigHostPath=/etc/cni/net.d/10-primary.conflist",
	} {
		args = append(args, "--set", setting)
	}
	output, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("render chart: %v\n%s", err, output)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(output)), 4096)
	seen := 0
	for {
		var ds appsv1.DaemonSet
		if err := decoder.Decode(&ds); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if ds.Kind != "DaemonSet" {
			continue
		}
		seen++
		if strings.HasSuffix(ds.Name, "-node-agent") {
			for _, volume := range ds.Spec.Template.Spec.Volumes {
				if !strings.HasPrefix(volume.Name, "cni-") {
					continue
				}
				if volume.HostPath == nil || volume.HostPath.Type == nil || !strings.HasPrefix(string(*volume.HostPath.Type), "Directory") {
					t.Fatalf("installation artifact uses a file bind mount: %#v", volume)
				}
			}
			container := ds.Spec.Template.Spec.Containers[0]
			for _, mount := range container.VolumeMounts {
				if strings.HasPrefix(mount.Name, "cni-") && (!mount.ReadOnly || mount.SubPath != "" || mount.SubPathExpr != "") {
					t.Fatalf("artifact directory is writable or pins a subpath inode: %#v", mount)
				}
			}
			for _, argument := range []string{
				"--cni-config-file=/var/run/waycloak-cni-install/config/05-waycloak.conflist",
				"--cni-receipt-file=/var/run/waycloak-cni-install/state/custom-receipt.json",
			} {
				if !containsArgument(container.Args, argument) {
					t.Fatalf("missing observer argument %q", argument)
				}
			}
		} else {
			for _, container := range ds.Spec.Template.Spec.InitContainers {
				if container.Name != "install" {
					continue
				}
				if !containsArgument(container.Args, "/host-state/custom-receipt.json") || container.Args[len(container.Args)-1] != "/host-config/10-primary.conflist" {
					t.Fatalf("installer did not receive exact source/receipt paths: %v", container.Args)
				}
			}
		}
	}
	if seen != 2 {
		t.Fatalf("rendered %d DaemonSets, expected 2", seen)
	}
}

func containsArgument(arguments []string, value string) bool {
	for _, argument := range arguments {
		if argument == value {
			return true
		}
	}
	return false
}
