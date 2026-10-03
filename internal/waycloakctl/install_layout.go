// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"errors"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// NodeInstallLayout binds host paths and node coverage independently of a
// release's executable identity. An ordinary transition must preserve both.
type NodeInstallLayout struct {
	ConfigPath            string            `json:"configPath"`
	BinaryPath            string            `json:"binaryPath"`
	ReceiptPath           string            `json:"receiptPath"`
	SourceConfigPath      string            `json:"sourceConfigPath,omitempty"`
	InstallerNodeSelector map[string]string `json:"installerNodeSelector,omitempty"`
	AgentNodeSelector     map[string]string `json:"agentNodeSelector,omitempty"`
}

func installerReleaseIdentity(container corev1.Container) (string, string, error) {
	if (len(container.Args) < 11 || len(container.Args) > 13) || container.Args[0] != "install" {
		return "", "", errors.New("CNI installer does not use the versioned install argument contract")
	}
	if len(container.Args) == 13 && container.Args[12] != "migrate-legacy-source" {
		return "", "", errors.New("CNI installer has an unknown migration mode")
	}
	return container.Args[9], container.Args[10], nil
}

func observeNodeInstallLayout(installer, agent corev1.PodSpec) (*NodeInstallLayout, error) {
	ic, err := requiredContainer(installer.InitContainers, "install")
	if err != nil {
		return nil, err
	}
	if _, _, err := installerReleaseIdentity(ic); err != nil {
		return nil, err
	}
	ac, err := requiredContainer(agent.Containers, "node-agent")
	if err != nil {
		return nil, err
	}
	result := &NodeInstallLayout{InstallerNodeSelector: copyStringMap(installer.NodeSelector), AgentNodeSelector: copyStringMap(agent.NodeSelector)}
	// Normalize empty maps so the identity is stable across API serialization.
	if len(result.InstallerNodeSelector) == 0 {
		result.InstallerNodeSelector = nil
	}
	if len(result.AgentNodeSelector) == 0 {
		result.AgentNodeSelector = nil
	}
	for _, item := range []struct {
		index       int
		flag        string
		destination *string
	}{
		{2, "--cni-binary-file=", &result.BinaryPath},
		{3, "--cni-config-file=", &result.ConfigPath},
		{4, "--cni-receipt-file=", &result.ReceiptPath},
	} {
		installed, err := mountedHostFile(installer, ic, ic.Args[item.index])
		if err != nil {
			return nil, err
		}
		argument, err := requiredArgument(ac.Args, item.flag)
		if err != nil {
			return nil, err
		}
		observed, err := mountedHostFile(agent, ac, argument)
		if err != nil {
			return nil, err
		}
		if installed != observed {
			return nil, errors.New("installer and node-agent host CNI paths disagree")
		}
		*item.destination = installed
	}
	if len(ic.Args) >= 12 {
		result.SourceConfigPath, err = mountedHostFile(installer, ic, ic.Args[11])
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Resolve both legacy file mounts and current directory mounts without reading
// host contents. Ambiguous mounts, subpaths, and traversal are not authority.
func mountedHostFile(pod corev1.PodSpec, container corev1.Container, filename string) (string, error) {
	if !path.IsAbs(filename) || path.Clean(filename) != filename {
		return "", errors.New("CNI path is not canonical and absolute")
	}
	result := ""
	for _, mount := range container.VolumeMounts {
		if filename != mount.MountPath && !strings.HasPrefix(filename, mount.MountPath+"/") {
			continue
		}
		if result != "" || mount.SubPath != "" || mount.SubPathExpr != "" {
			return "", errors.New("CNI path has ambiguous or subpath mounts")
		}
		for _, volume := range pod.Volumes {
			if volume.Name != mount.Name {
				continue
			}
			if volume.HostPath == nil || !path.IsAbs(volume.HostPath.Path) || path.Clean(volume.HostPath.Path) != volume.HostPath.Path {
				return "", errors.New("CNI path lacks a canonical host mount")
			}
			if result != "" {
				return "", errors.New("CNI path has duplicate host volumes")
			}
			result = path.Join(volume.HostPath.Path, strings.TrimPrefix(filename, mount.MountPath))
		}
	}
	if result == "" {
		return "", fmt.Errorf("CNI file %q has no host mount", filename)
	}
	return result, nil
}
