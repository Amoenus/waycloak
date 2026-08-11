// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

func Preflight(ctx context.Context, clients *Clients, overlayCIDR string) (PreflightReport, error) {
	report := PreflightReport{
		APIVersion: OutputAPIVersion, Kind: "PreflightReport", Compatible: true,
		Profile: "networking.waycloak.io/Core-v1", Networking: NetworkSummary{OverlayCIDR: overlayCIDR},
		Identity: ClusterIdentity{ServerFingerprint: clients.ClusterServerFingerprint, TrustFingerprint: clients.ClusterTrustFingerprint},
	}
	if !validDigest(report.Identity.ServerFingerprint) || !validDigest(report.Identity.TrustFingerprint) {
		return report, fmt.Errorf("trusted cluster server and CA fingerprints are required")
	}
	system, err := clients.Kubernetes.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return report, fmt.Errorf("resolve cluster UID anchor: %w", err)
	}
	if system.UID == "" {
		return report, fmt.Errorf("resolve cluster UID anchor: kube-system UID is empty")
	}
	report.Identity.ClusterUIDFingerprint = fingerprintText(string(system.UID))
	version, err := clients.Discovery.ServerVersion()
	if err != nil {
		return report, fmt.Errorf("discover Kubernetes version: %w", err)
	}
	report.Cluster.KubernetesVersion = version.GitVersion
	minor, minorErr := strconv.Atoi(strings.TrimSuffix(version.Minor, "+"))
	report.add("kubernetes-version", minorErr == nil && version.Major == "1" && minor >= 36, "Kubernetes "+version.GitVersion+" is eligible for an exact published support row", "Use a published Kubernetes 1.36+ support-matrix row")

	nodes, err := clients.Kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return report, fmt.Errorf("list nodes: %w", err)
	}
	report.Cluster.NodeCount = len(nodes.Items)
	report.Cluster.Architectures, report.Cluster.Runtimes = map[string]int{}, map[string]int{}
	report.Cluster.RuntimeVersions, report.Cluster.Kernels, report.Cluster.OperatingSystems = map[string]int{}, map[string]int{}, map[string]int{}
	nodesSupported := len(nodes.Items) > 0
	podCIDRs := map[string]struct{}{}
	overlay, overlayErr := netip.ParsePrefix(overlayCIDR)
	for _, node := range nodes.Items {
		info := node.Status.NodeInfo
		report.Cluster.Architectures[info.Architecture]++
		report.Cluster.Runtimes[normalizedRuntime(info.ContainerRuntimeVersion)]++
		report.Cluster.RuntimeVersions[info.ContainerRuntimeVersion]++
		report.Cluster.Kernels[info.KernelVersion]++
		report.Cluster.OperatingSystems[info.OperatingSystem]++
		if info.OperatingSystem != "linux" || info.Architecture != "amd64" && info.Architecture != "arm64" || !supportedKernel(info.KernelVersion) {
			nodesSupported = false
		}
		for _, value := range node.Spec.PodCIDRs {
			podCIDRs[value] = struct{}{}
			if prefix, parseErr := netip.ParsePrefix(value); overlayErr == nil && parseErr == nil && prefixesOverlap(overlay, prefix) {
				overlayErr = fmt.Errorf("overlay overlaps a Pod CIDR")
			}
		}
	}
	for value := range podCIDRs {
		report.Networking.PodCIDRs = append(report.Networking.PodCIDRs, value)
	}
	sort.Strings(report.Networking.PodCIDRs)
	report.add("nodes", nodesSupported, "All nodes report an eligible Linux kernel and architecture", "Use Linux amd64/arm64 nodes with kernel 5.10+ and a published runtime row")
	report.add("overlay", overlayErr == nil, "The reviewed overlay CIDR does not overlap observed Pod CIDRs", "Choose an explicit private IPv4 CIDR that does not overlap Pod, Service, node, or VPN networks")

	daemonSets, err := clients.Kubernetes.AppsV1().DaemonSets("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return report, fmt.Errorf("inspect kube-system CNI: %w", err)
	}
	report.CNI = detectCNI(daemonSets.Items, version.GitVersion)
	report.add("chained-cni", report.CNI.Name != "" && validDigest(report.CNI.IdentityDigest), "An eligible chained-CNI layout was detected", "Use the explicit advanced path or a published Kind/k3s-Flannel support row")

	services, err := clients.Kubernetes.CoreV1().Services("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return report, fmt.Errorf("inspect cluster DNS: %w", err)
	}
	var dnsServiceIPs []string
	for _, service := range services.Items {
		if service.Labels["k8s-app"] == "kube-dns" || service.Name == "kube-dns" || service.Name == "coredns" {
			if address, parseErr := netip.ParseAddr(service.Spec.ClusterIP); parseErr == nil && address.Is4() {
				dnsServiceIPs = append(dnsServiceIPs, address.String())
			}
		}
	}
	sort.Strings(dnsServiceIPs)
	dnsServiceIPs = compactStrings(dnsServiceIPs)
	if len(dnsServiceIPs) == 1 {
		report.Networking.DNSServiceIP = dnsServiceIPs[0]
	}
	configMaps, err := clients.Kubernetes.CoreV1().ConfigMaps("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return report, fmt.Errorf("inspect cluster DNS configuration: %w", err)
	}
	domains := map[string]struct{}{}
	for _, configMap := range configMaps.Items {
		contents, exists := configMap.Data["Corefile"]
		if !exists {
			continue
		}
		for _, domain := range coreDNSClusterDomains(contents) {
			domains[domain] = struct{}{}
		}
	}
	if len(domains) == 1 {
		for domain := range domains {
			report.Networking.ClusterDomain = domain
		}
	}
	report.Networking.DNSObserved = report.Networking.DNSServiceIP != "" && report.Networking.ClusterDomain != ""
	report.add("cluster-dns", report.Networking.DNSObserved, "One reviewed CoreDNS Service address and cluster domain are observable", "Install or repair one unambiguous CoreDNS Service and kubernetes zone before Waycloak")

	resources, err := clients.Discovery.ServerResourcesForGroupVersion("admissionregistration.k8s.io/v1")
	declarativeAdmission := err == nil && hasResource(resources.APIResources, "validatingadmissionpolicies") && hasResource(resources.APIResources, "mutatingadmissionpolicies")
	report.add("declarative-admission", declarativeAdmission, "Stable validating and mutating admission policy APIs are served", "Use a supported Kubernetes version with stable admission policy APIs")

	crds, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		return report, fmt.Errorf("inspect existing CRDs: %w", err)
	}
	alphaFound := false
	for _, crd := range crds.Items {
		if crd.Spec.Group != "networking.waycloak.io" {
			continue
		}
		for _, version := range crd.Spec.Versions {
			if version.Served && version.Name == "v1alpha1" {
				alphaFound = true
			}
		}
	}
	report.add("clean-api", !alphaFound, "No served alpha Waycloak API was detected", "Stop protected workloads and follow the confirmation-gated alpha purge runbook; never install replacement CRDs over alpha")
	report.ObservationDigest, err = preflightObservationDigest(report)
	if err != nil {
		return report, err
	}
	return report, nil
}

func coreDNSClusterDomains(contents string) []string {
	set := map[string]struct{}{}
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		fields := strings.Fields(line)
		for index, field := range fields {
			if field != "kubernetes" {
				continue
			}
			for _, candidate := range fields[index+1:] {
				candidate = strings.TrimSuffix(strings.ToLower(candidate), ".")
				if candidate == "{" {
					break
				}
				if strings.HasSuffix(candidate, ".in-addr.arpa") || candidate == "in-addr.arpa" || strings.HasSuffix(candidate, ".ip6.arpa") || candidate == "ip6.arpa" {
					continue
				}
				if len(utilvalidation.IsDNS1123Subdomain(candidate)) == 0 {
					set[candidate] = struct{}{}
				}
			}
		}
	}
	result := make([]string, 0, len(set))
	for domain := range set {
		result = append(result, domain)
	}
	sort.Strings(result)
	return result
}

func compactStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func preflightObservationDigest(report PreflightReport) (string, error) {
	report.ObservationDigest = ""
	data, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("encode preflight observation: %w", err)
	}
	return digestBytes(data), nil
}

func (report *PreflightReport) add(name string, passed bool, success, remediation string) {
	status, summary := "Pass", success
	if !passed {
		status, summary, report.Compatible = "Fail", strings.TrimPrefix(remediation, "Use "), false
	}
	report.Checks = append(report.Checks, Check{Name: name, Status: status, Summary: summary, Remediation: choose(!passed, remediation)})
}

func detectCNI(items []appsv1.DaemonSet, kubernetesVersion string) CNISummary {
	alternate := false
	for _, item := range items {
		for _, container := range append(item.Spec.Template.Spec.InitContainers, item.Spec.Template.Spec.Containers...) {
			identity := strings.ToLower(item.Name + " " + container.Name + " " + container.Image)
			switch {
			case strings.Contains(identity, "kindnet"):
				return CNISummary{Name: "kindnet", ConfigPath: "/etc/cni/net.d/10-kindnet.conflist", BinaryPath: "/opt/cni/bin/waycloak-cni", IdentityDigest: cniDaemonSetDigest(item)}
			case strings.Contains(identity, "flannel"):
				return CNISummary{Name: "flannel", ConfigPath: "/etc/cni/net.d/10-flannel.conflist", BinaryPath: "/opt/cni/bin/waycloak-cni", IdentityDigest: cniDaemonSetDigest(item)}
			case strings.Contains(identity, "calico"), strings.Contains(identity, "cilium"), strings.Contains(identity, "canal"), strings.Contains(identity, "weave"):
				alternate = true
			}
		}
	}
	if strings.Contains(strings.ToLower(kubernetesVersion), "k3s") && !alternate {
		return CNISummary{Name: "k3s-flannel", ConfigPath: "/var/lib/rancher/k3s/agent/etc/cni/net.d/10-flannel.conflist", BinaryPath: "/var/lib/rancher/k3s/data/cni/waycloak-cni", IdentityDigest: digestBytes([]byte("k3s-flannel\x00" + kubernetesVersion))}
	}
	return CNISummary{}
}

func cniDaemonSetDigest(item appsv1.DaemonSet) string {
	type containerIdentity struct {
		Name  string   `json:"name"`
		Image string   `json:"image"`
		Args  []string `json:"args,omitempty"`
	}
	identity := struct {
		Namespace  string              `json:"namespace"`
		Name       string              `json:"name"`
		UID        string              `json:"uid,omitempty"`
		Generation int64               `json:"generation"`
		Containers []containerIdentity `json:"containers"`
	}{Namespace: item.Namespace, Name: item.Name, UID: string(item.UID), Generation: item.Generation}
	for _, container := range append(item.Spec.Template.Spec.InitContainers, item.Spec.Template.Spec.Containers...) {
		identity.Containers = append(identity.Containers, containerIdentity{Name: container.Name, Image: container.Image, Args: container.Args})
	}
	data, _ := json.Marshal(identity)
	return digestBytes(data)
}

func normalizedRuntime(value string) string {
	if index := strings.IndexByte(value, ':'); index >= 0 {
		return value[:index]
	}
	return value
}

func supportedKernel(value string) bool {
	parts := strings.SplitN(value, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	return majorErr == nil && minorErr == nil && (major > 5 || major == 5 && minor >= 10)
}

func prefixesOverlap(a, b netip.Prefix) bool { return a.Contains(b.Addr()) || b.Contains(a.Addr()) }

func hasResource(resources []metav1.APIResource, name string) bool {
	for _, resource := range resources {
		if resource.Name == name {
			return true
		}
	}
	return false
}

func choose(condition bool, value string) string {
	if condition {
		return value
	}
	return ""
}
