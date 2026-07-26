// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestReplacementAPIFreshInstall(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_REPLACEMENT_API") != "1" {
		t.Skip("set WAYCLOAK_E2E_REPLACEMENT_API=1 to install and verify the replacement API")
	}
	contextName := strings.TrimSpace(command(t, nil, "kubectl", "config", "current-context"))
	if !strings.HasPrefix(contextName, "kind-") && os.Getenv("WAYCLOAK_E2E_ALLOW_NON_KIND") != "1" {
		t.Skip("set WAYCLOAK_E2E_ALLOW_NON_KIND=1 to authorize a non-Kind cluster")
	}

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	namespace := "waycloak-api-" + suffix
	release := "waycloak-api-" + suffix
	className := "e2e-" + suffix + ".waycloak.test"
	chartPath := filepath.Join("..", "..", "charts", "waycloak")

	command(t, nil, "kubectl", "create", "namespace", namespace)
	t.Cleanup(func() {
		_ = exec.Command("helm", "uninstall", release, "--namespace", namespace).Run()
		_ = exec.Command("kubectl", "delete", "vpngatewayclass", className, "--ignore-not-found", "--wait=true", "--timeout=30s").Run()
		_ = exec.Command("kubectl", "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=30s").Run()
	})

	command(t, nil, "helm", "upgrade", "--install", release, chartPath,
		"--namespace", namespace,
		"--wait", "--timeout", "3m")

	wantResources := []string{
		"portforwardleases.networking.waycloak.io",
		"vpnegressroutes.networking.waycloak.io",
		"vpngatewayclasses.networking.waycloak.io",
		"vpngateways.networking.waycloak.io",
		"vpnworkloadbindings.networking.waycloak.io",
		"workloadadapters.networking.waycloak.io",
	}
	gotResources := strings.Fields(command(t, nil, "kubectl", "api-resources", "--api-group=networking.waycloak.io", "-o", "name"))
	sort.Strings(gotResources)
	if strings.Join(gotResources, "\n") != strings.Join(wantResources, "\n") {
		t.Fatalf("replacement discovery resources = %q, want %q", gotResources, wantResources)
	}
	assertCommandFails(t, "v1alpha1 discovery remained served", nil, "kubectl", "get", "--raw", "/apis/networking.waycloak.io/v1alpha1")
	assertCommandFails(t, "removed VPNWorkload remained discoverable", nil, "kubectl", "get", "vpnworkloads.networking.waycloak.io", "-A")

	label := "app.kubernetes.io/instance=" + release
	if output := strings.TrimSpace(command(t, nil, "kubectl", "get", "deployments", "-n", namespace, "-l", label, "-o", "name")); output != "" {
		t.Fatalf("API-only chart installed a runtime Deployment: %s", output)
	}
	for _, resource := range []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"} {
		if output := strings.TrimSpace(command(t, nil, "kubectl", "get", resource, "-l", label, "-o", "name")); output != "" {
			t.Fatalf("API-only chart installed %s: %s", resource, output)
		}
	}
	for _, resource := range []string{"validatingadmissionpolicy", "validatingadmissionpolicybinding"} {
		command(t, nil, "kubectl", "get", resource, release+"-binding-guard")
	}
	for _, role := range []string{
		"waycloak-distribution",
		"waycloak-network-operator",
		"waycloak-workload-owner",
		"waycloak-adapter-operator",
		"waycloak-node-agent",
	} {
		command(t, nil, "kubectl", "get", "clusterrole", role)
	}

	class := fmt.Sprintf(`apiVersion: networking.waycloak.io/v1beta1
kind: VPNGatewayClass
metadata:
  name: %s
spec:
  controllerName: e2e.waycloak.test/controller
  releaseIdentity:
    version: v1.0.0-beta.1
    manifestDigest: sha256:1111111111111111111111111111111111111111111111111111111111111111
  supportedFeatures:
    - networking.waycloak.io/CoreFailClosedEgress
    - networking.waycloak.io/TCP
    - networking.waycloak.io/UDP
    - networking.waycloak.io/DNSContainment
    - networking.waycloak.io/GatewayReplacementRecovery
    - networking.waycloak.io/NodeRestartRecovery
  conformanceProfile: networking.waycloak.io/Core-v1
`, className)
	applyInput(t, nil, class)

	gatewayAndRoute := fmt.Sprintf(`apiVersion: networking.waycloak.io/v1beta1
kind: VPNGateway
metadata:
  name: private
  namespace: %s
spec:
  gatewayClassName: %s
  requestedFeatures:
    - networking.waycloak.io/CoreFailClosedEgress
  clusterTraffic:
    mode: TunnelAll
  dns:
    mode: Gateway
---
apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: private
  namespace: %s
spec:
  parentRefs:
    - group: networking.waycloak.io
      kind: VPNGateway
      namespace: %s
      name: private
`, namespace, className, namespace, namespace)
	applyInput(t, nil, gatewayAndRoute)

	invalidRoute := fmt.Sprintf(`apiVersion: networking.waycloak.io/v1beta1
kind: VPNEgressRoute
metadata:
  name: invalid
  namespace: %s
spec:
  parentRefs: []
`, namespace)
	assertApplyFails(t, "API server accepted a route without a parent", nil, invalidRoute)

	binding := fmt.Sprintf(`apiVersion: networking.waycloak.io/v1beta1
kind: VPNWorkloadBinding
metadata:
  name: protected-11111111
  namespace: %s
spec:
  podRef:
    name: protected
    uid: 11111111-1111-1111-1111-111111111111
  routeRef:
    name: private
    uid: 22222222-2222-2222-2222-222222222222
  gatewayRef:
    namespace: %s
    name: private
    uid: 33333333-3333-3333-3333-333333333333
  nodeName: worker-1
  allocation:
    identity: allocation-1
    address: 100.64.0.2/32
`, namespace, namespace)
	assertApplyFails(t, "ordinary user created a controller-authored binding", nil, binding)
	controllerUser := "system:serviceaccount:" + namespace + ":" + release
	applyInput(t, []string{"--as=" + controllerUser}, binding)
	command(t, nil, "kubectl", "get", "vpnworkloadbinding", "protected-11111111", "-n", namespace)
	command(t, nil, "kubectl", "delete", "vpnworkloadbinding", "protected-11111111", "-n", namespace, "--as="+controllerUser, "--wait=true", "--timeout=30s")
}

func applyInput(t *testing.T, prefixArgs []string, manifest string) {
	t.Helper()
	args := append(append([]string{}, prefixArgs...), "apply", "--server-side", "--field-manager=waycloak-e2e", "-f", "-")
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = strings.NewReader(manifest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s", args, err, output)
	}
}

func assertApplyFails(t *testing.T, failureMessage string, prefixArgs []string, manifest string) {
	t.Helper()
	args := append(append([]string{}, prefixArgs...), "apply", "--server-side", "--field-manager=waycloak-e2e", "-f", "-")
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = strings.NewReader(manifest)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("%s: %s", failureMessage, output)
	}
}

func assertCommandFails(t *testing.T, failureMessage string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if env != nil {
		cmd.Env = env
	}
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("%s: %s", failureMessage, output)
	}
}
