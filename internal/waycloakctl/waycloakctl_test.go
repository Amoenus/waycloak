// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestPreflightSupportsPinnedK3sAndRejectsServedAlpha(t *testing.T) {
	clients := supportedClients(t)
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || !report.Compatible || report.CNI.Name != "k3s-flannel" {
		t.Fatalf("supported preflight failed: %#v %v", report, err)
	}
	if report.Networking.DNSServiceIP != "10.43.0.10" || report.Networking.ClusterDomain != "cluster.local" || !report.Networking.DNSObserved {
		t.Fatalf("reviewed split-DNS identity was not observed: %#v", report.Networking)
	}
	alpha := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "vpngateways.networking.waycloak.io"}, Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: "networking.waycloak.io", Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "vpngateways", Kind: "VPNGateway"}, Scope: apiextensionsv1.NamespaceScoped, Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true}}}}
	clients.APIExtensions = apiextensionsfake.NewSimpleClientset(alpha)
	report, err = Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || report.Compatible || checkStatus(report, "clean-api") != "Fail" {
		t.Fatalf("served alpha API was not rejected: %#v %v", report, err)
	}
}

func TestPreflightRejectsOverlayOverlapAndUnsupportedNode(t *testing.T) {
	clients := supportedClients(t)
	node, _ := clients.Kubernetes.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	node.Status.NodeInfo.Architecture = "s390x"
	_, _ = clients.Kubernetes.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
	report, err := Preflight(context.Background(), clients, "10.42.0.0/16")
	if err != nil || report.Compatible || checkStatus(report, "nodes") != "Fail" || checkStatus(report, "overlay") != "Fail" {
		t.Fatalf("unsafe node/network passed: %#v %v", report, err)
	}
}

func TestPreflightRejectsAmbiguousClusterDNSIdentity(t *testing.T) {
	clients := supportedClients(t)
	_, err := clients.Kubernetes.CoreV1().Services("kube-system").Create(context.Background(), &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"}, Spec: corev1.ServiceSpec{ClusterIP: "10.43.0.11"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || report.Compatible || checkStatus(report, "cluster-dns") != "Fail" || report.Networking.DNSServiceIP != "" {
		t.Fatalf("ambiguous DNS Service identity passed preflight: %#v %v", report, err)
	}
}

func TestCoreDNSClusterDomainParserRejectsReverseAndInvalidZones(t *testing.T) {
	contents := ".:53 {\n  kubernetes Cluster.Local. in-addr.arpa ip6.arpa {\n  }\n}\nexample:53 { kubernetes INVALID_ZONE { } }\n"
	if got := coreDNSClusterDomains(contents); !reflect.DeepEqual(got, []string{"cluster.local"}) {
		t.Fatalf("cluster domains = %#v", got)
	}
}

func TestPreflightObservationBindsClusterIdentityAndNodeFacts(t *testing.T) {
	clients := supportedClients(t)
	first, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || !validDigest(first.ObservationDigest) || !validDigest(first.Identity.ClusterUIDFingerprint) {
		t.Fatalf("preflight lacks canonical cluster identity: %#v %v", first, err)
	}
	second, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || second.ObservationDigest != first.ObservationDigest {
		t.Fatalf("unchanged preflight identity drifted: %s != %s: %v", first.ObservationDigest, second.ObservationDigest, err)
	}
	node, err := clients.Kubernetes.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.NodeInfo.KernelVersion = "6.9.0"
	if _, err = clients.Kubernetes.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	changed, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil || changed.ObservationDigest == first.ObservationDigest {
		t.Fatalf("node fact change retained preflight identity: %#v %v", changed, err)
	}
}

func TestInstallPlanRequiresExplicitArchitectureOnMixedCluster(t *testing.T) {
	clients := supportedClients(t)
	arm := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "arm"}, Spec: corev1.NodeSpec{PodCIDRs: []string{"10.42.1.0/24"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "arm64", OperatingSystem: "linux", KernelVersion: "6.8.0", ContainerRuntimeVersion: "containerd://2.1.0"}}}
	if _, err := clients.Kubernetes.CoreV1().Nodes().Create(context.Background(), arm, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	source, crds := absentInstallInputs(t)
	if _, err = BuildInstallPlan(releaseManifest(), "waycloak-system", "waycloak", "", report, source, crds, nil); err == nil || !strings.Contains(err.Error(), "explicit --node-architecture") {
		t.Fatalf("mixed cluster did not require an explicit row: %v", err)
	}
	plan, err := BuildInstallPlan(releaseManifest(), "waycloak-system", "waycloak", "amd64", report, source, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NodeArchitecture != "amd64" || strings.Count(plan.Values, "kubernetes.io/arch: \"amd64\"") != 2 {
		t.Fatalf("plan did not constrain both node components: %#v\n%s", plan, plan.Values)
	}
}

func TestInstallPlanHasNoCredentialValuesAndRequiresExactConfirmation(t *testing.T) {
	manifest := releaseManifest()
	report, err := Preflight(context.Background(), supportedClients(t), "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	source, crds := absentInstallInputs(t)
	plan, err := BuildInstallPlan(manifest, "waycloak-system", "waycloak", "", report, source, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	nestedIdentity := "  releaseIdentity:\n    version: " + strconv.Quote(manifest.Version) + "\n    manifestDigest: " + strconv.Quote(manifest.ManifestDigest) + "\n"
	if count := strings.Count(plan.Values, nestedIdentity); count != 2 {
		t.Fatalf("install values contain %d nested runtime release identities, want node agent and default class", count)
	}
	cniIdentity := "  cniReleaseIdentity:\n    version: " + strconv.Quote(manifest.Version) + "\n    manifestDigest: " + strconv.Quote(manifest.ManifestDigest) + "\n"
	if count := strings.Count(plan.Values, cniIdentity); count != 1 {
		t.Fatalf("install values contain %d installed CNI release identities, want one exact node-agent validation identity", count)
	}
	if !strings.Contains(plan.Values, "serviceIP: \"10.43.0.10\"") || !strings.Contains(plan.Values, "domain: \"cluster.local\"") {
		t.Fatalf("install values do not bind reviewed split DNS:\n%s", plan.Values)
	}
	if !strings.Contains(plan.Values, `observationRelayURL: "https://waycloak-controller.waycloak-system.svc:9443/node-observations/v1/report"`) {
		t.Fatalf("install values do not use the controller observation relay contract: %s", plan.Values)
	}
	if plan.InstallSequence != failClosedLifecycleSequence || len(plan.Commands) != 4 {
		t.Fatalf("install plan does not expose the fail-closed lifecycle sequence: %#v", plan)
	}
	encoded, err := EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"username", "password", "privateKey", "latest"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("plan contains forbidden value %q", forbidden)
		}
	}
	clients := supportedClients(t)
	if err := ApplyInstallPlan(context.Background(), clients, nil, plan, "wrong"); err == nil || !strings.Contains(err.Error(), "refusing mutation") {
		t.Fatalf("wrong confirmation did not fail: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().Namespaces().Get(context.Background(), "waycloak-system", metav1.GetOptions{}); !errorsIsNotFound(err) {
		t.Fatalf("refused apply mutated the cluster: %v", err)
	}
}

func TestReleaseManifestIdentityRejectsTamperingAndExtraArtifacts(t *testing.T) {
	manifest := releaseManifest()
	manifest.Version = "v1.0.0-tampered"
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "canonical identity") {
		t.Fatalf("tampered manifest identity was accepted: %v", err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release-manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadReleaseManifest(path); err == nil || !strings.Contains(err.Error(), "canonical identity") {
		t.Fatalf("loader accepted a tampered manifest identity: %v", err)
	}

	manifest = releaseManifest()
	manifest.Images["unreviewed-backend"] = Artifact{Repository: "example.invalid/unreviewed", Digest: "sha256:" + strings.Repeat("9", 64)}
	digest, err := manifest.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestDigest = digest
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "complete Waycloak artifact set") {
		t.Fatalf("extra release artifact was accepted: %v", err)
	}

	manifest = releaseManifest()
	delete(manifest.Images, "waycloak-gateway-runtime")
	delete(manifest.Images, "waycloak-qbittorrent-adapter")
	manifest.Images["unknown-one"] = Artifact{Repository: "example.invalid/unknown-one", Digest: "sha256:" + strings.Repeat("5", 64)}
	manifest.Images["unknown-two"] = Artifact{Repository: "example.invalid/unknown-two", Digest: "sha256:" + strings.Repeat("6", 64)}
	digest, err = manifest.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestDigest = digest
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "unknown image") {
		t.Fatalf("count-matching unknown release inventory was accepted: %v", err)
	}

	manifest = releaseManifest()
	delete(manifest.Images, "waycloak-qbittorrent-adapter")
	digest, err = manifest.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestDigest = digest
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "complete Waycloak artifact set") {
		t.Fatalf("partial port-forward release inventory was accepted: %v", err)
	}

	manifest = releaseManifest()
	manifest.Profiles = append(manifest.Profiles, "networking.waycloak.io/PortForwardServiceSingleActive")
	digest, err = manifest.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestDigest = digest
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "baseline conformance only") {
		t.Fatalf("optional capability was accepted as a separate release profile: %v", err)
	}
}

func TestReleaseManifestSupportMatrixValidationAndCanonicalIdentity(t *testing.T) {
	manifest := releaseManifest()
	original := manifest.ManifestDigest
	row := &manifest.SupportMatrix.Rows[0]
	row.Features[0], row.Features[1] = row.Features[1], row.Features[0]
	row.EvidenceSuites[0], row.EvidenceSuites[1] = row.EvidenceSuites[1], row.EvidenceSuites[0]
	digest, err := manifest.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != original {
		t.Fatalf("support set order changed canonical identity: got %s, want %s", digest, original)
	}

	cases := []struct {
		name   string
		mutate func(*ReleaseManifest)
		want   string
	}{
		{"empty", func(candidate *ReleaseManifest) { candidate.SupportMatrix.Rows = nil }, "at least one row"},
		{"duplicate-row", func(candidate *ReleaseManifest) {
			candidate.SupportMatrix.Rows = append(candidate.SupportMatrix.Rows, candidate.SupportMatrix.Rows[0])
		}, "duplicated"},
		{"incomplete-row", func(candidate *ReleaseManifest) { candidate.SupportMatrix.Rows[0].Runtime = "" }, "incomplete"},
		{"empty-features", func(candidate *ReleaseManifest) { candidate.SupportMatrix.Rows[0].Features = nil }, "features must not be empty"},
		{"duplicate-evidence", func(candidate *ReleaseManifest) {
			candidate.SupportMatrix.Rows[0].EvidenceSuites = []string{"same", "same"}
		}, "evidence suites must be unique"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := releaseManifest()
			test.mutate(&candidate)
			candidate.ManifestDigest, err = candidate.IdentityDigest()
			if err != nil {
				t.Fatal(err)
			}
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid support matrix accepted: %v", err)
			}
		})
	}
}

func TestPortForwardInstallPlanBindsExactRuntimeAndTLSIdentity(t *testing.T) {
	manifest := portForwardReleaseManifest()
	report, err := Preflight(context.Background(), supportedClients(t), "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	source, crds := absentInstallInputs(t)
	identity := &PortForwardInstallIdentity{ControllerTLSSecret: "waycloak-port-forward-controller-tls", SecretUID: "tls-uid", CADigest: "sha256:" + strings.Repeat("7", 64), CertificateDigest: "sha256:" + strings.Repeat("8", 64), AdapterProtocolEnabled: true}
	plan, err := BuildInstallPlan(manifest, "waycloak-system", "waycloak", "", report, source, crds, identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"portForwarding:\n  enabled: true", `controllerTLSSecret: "waycloak-port-forward-controller-tls"`, `repository: "ghcr.io/amoenus/waycloak-gateway-runtime"`, "adapter:\n    enabled: true", `conformanceProfile: "networking.waycloak.io/Core-v1"`} {
		if !strings.Contains(plan.Values, expected) {
			t.Fatalf("port-forward install values lack %q:\n%s", expected, plan.Values)
		}
	}
	encoded, err := EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "tls.key") || strings.Contains(string(encoded), "PRIVATE KEY") {
		t.Fatalf("port-forward plan exposed private key material: %s", encoded)
	}

	deployedClients := supportedClients(t)
	testCRDs, deployedCRDs, _ := testInstallCRDBundle(t)
	if _, _, err = ensureObservationSecrets(context.Background(), deployedClients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("9", 64)); err != nil {
		t.Fatal(err)
	}
	seedInstalledRelease(t, deployedClients, manifest, "waycloak-system", "waycloak", 1, testCRDs)
	deployed, err := ObserveInstalledRelease(context.Background(), deployedClients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	deployedReport, err := Preflight(context.Background(), deployedClients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BuildInstallPlan(manifest, "waycloak-system", "waycloak", "", deployedReport, deployed, deployedCRDs, identity); err == nil || !strings.Contains(err.Error(), "changed exact release") {
		t.Fatalf("same-release port-forward activation bypassed class replacement: %v", err)
	}
}

func TestPortForwardInstallPlanFlagsRequireCompleteExplicitActivation(t *testing.T) {
	for name, arguments := range map[string][]string{
		"adapter without runtime": {"plan", "--release-manifest", "unused", "--enable-adapter-protocol"},
		"runtime without secret":  {"plan", "--release-manifest", "unused", "--enable-port-forwarding"},
		"secret without runtime":  {"plan", "--release-manifest", "unused", "--port-forward-controller-tls-secret", "identity"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := runInstall(context.Background(), arguments, Dependencies{Stderr: io.Discard}); err == nil || !strings.Contains(err.Error(), "requires --enable-port-forwarding") && !strings.Contains(err.Error(), "must be supplied together") {
				t.Fatalf("incomplete port-forward activation flags reached cluster discovery: %v", err)
			}
		})
	}
}

func TestInstallPlanRejectsRemovedTierFlags(t *testing.T) {
	for _, removed := range []string{"--enable-extended", "--extended-controller-tls-secret", "--enable-workload-adapter", "--enable-qbittorrent-adapter"} {
		err := runInstall(context.Background(), []string{"plan", "--release-manifest", "unused", removed}, Dependencies{Stderr: io.Discard})
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("removed flag %s was not rejected: %v", removed, err)
		}
	}
}

func TestObservationCapabilityHoldIdentityIsExactAndSingular(t *testing.T) {
	planID := "sha256:" + strings.Repeat("a", 64)
	held, err := observationCapabilityHeld([]string{"--observation-capability-hold=true", "--observation-capability-hold-id=" + planID})
	identity, identityErr := observationCapabilityHoldID([]string{"--observation-capability-hold=true", "--observation-capability-hold-id=" + planID})
	if err != nil || identityErr != nil || !held || identity != planID {
		t.Fatalf("exact transition hold was not observed: held=%t identity=%q err=%v identityErr=%v", held, identity, err, identityErr)
	}
	for name, arguments := range map[string][]string{
		"mutable identity": {"--observation-capability-hold-id=latest"},
		"duplicate hold":   {"--observation-capability-hold=true", "--observation-capability-hold=true"},
		"duplicate identity": {
			"--observation-capability-hold-id=" + planID,
			"--observation-capability-hold-id=" + planID,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, holdErr := observationCapabilityHeld(arguments); holdErr == nil {
				if _, idErr := observationCapabilityHoldID(arguments); idErr == nil {
					t.Fatal("ambiguous transition hold identity was accepted")
				}
			}
		})
	}
}

func TestPortForwardInstallApplyRejectsTLSIdentitySwapBeforeMutation(t *testing.T) {
	clients := supportedClients(t)
	ca, certificate, key := portForwardControllerIdentity(t)
	immutable := true
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "port-forward-controller-tls", Namespace: "waycloak-system", UID: "tls-uid"}, Immutable: &immutable, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"ca.crt": ca, "tls.crt": certificate, "tls.key": key}}
	if _, err := clients.Kubernetes.CoreV1().Secrets(secret.Namespace).Create(context.Background(), secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	identity, err := observePortForwardInstallIdentity(context.Background(), clients, secret.Namespace, secret.Name, true)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	source, crds := absentInstallInputs(t)
	plan, err := BuildInstallPlan(portForwardReleaseManifest(), secret.Namespace, "waycloak", "", report, source, crds, &identity)
	if err != nil {
		t.Fatal(err)
	}
	secret, err = clients.Kubernetes.CoreV1().Secrets(secret.Namespace).Get(context.Background(), secret.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data["ca.crt"] = []byte("swapped")
	if _, err = clients.Kubernetes.CoreV1().Secrets(secret.Namespace).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	runnerCalled := false
	err = ApplyInstallPlan(context.Background(), clients, func(context.Context, string, ...string) ([]byte, error) {
		runnerCalled = true
		return nil, nil
	}, plan, plan.PlanID)
	if err == nil || !strings.Contains(err.Error(), "refusing mutation") || runnerCalled {
		t.Fatalf("TLS identity swap was not rejected before mutation: called=%t err=%v", runnerCalled, err)
	}
}

func TestPortForwardInstallRejectsWrongTLSRoleAndIdentity(t *testing.T) {
	clients := supportedClients(t)
	ca, certificate, key, err := observationIdentity("port-forward", "waycloak-system")
	if err != nil {
		t.Fatal(err)
	}
	immutable := true
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "wrong-role", Namespace: "waycloak-system", UID: "wrong-role-uid"}, Immutable: &immutable, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"ca.crt": ca, "tls.crt": certificate, "tls.key": key}}
	if _, err = clients.Kubernetes.CoreV1().Secrets(secret.Namespace).Create(context.Background(), secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = observePortForwardInstallIdentity(context.Background(), clients, secret.Namespace, secret.Name, false); err == nil || !strings.Contains(err.Error(), "client authentication") {
		t.Fatalf("server-only non-SPIFFE certificate passed port-forward planning: %v", err)
	}
}

func TestInstallApplyCreatesInMemoryTLSAndRejectsTampering(t *testing.T) {
	manifest := releaseManifest()
	clients := supportedClients(t)
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	testCRDs, crds, crdBundle := testInstallCRDBundle(t)
	source, err := ObserveInstalledRelease(context.Background(), clients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildInstallPlan(manifest, "waycloak-system", "waycloak", "", report, source, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	encodedPlan, err := EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(t.TempDir(), "install-plan.json")
	if err = os.WriteFile(planPath, encodedPlan, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err = LoadInstallPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	upgradeCalls := 0
	activeManifest := manifest
	stageSource := source
	activeTransitionPlan := InstallPlan{}
	revision := int64(0)
	runner := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name != "helm" {
			t.Fatalf("unexpected Helm command: %s %#v", name, arguments)
		}
		if len(arguments) >= 2 && arguments[0] == "show" && arguments[1] == "crds" {
			return crdBundle, nil
		}
		if !containsString(arguments, "@"+activeManifest.Chart.Digest) {
			t.Fatalf("unexpected Helm chart: %#v", arguments)
		}
		upgradeCalls++
		values := make([]string, 0, 2)
		for index, argument := range arguments {
			if argument == "--values" && index+1 < len(arguments) {
				data, err := os.ReadFile(arguments[index+1])
				if err != nil {
					t.Fatal(err)
				}
				values = append(values, string(data))
			}
		}
		if upgradeCalls == 1 && (len(values) != 2 || values[1] != controllerFirstBootstrapValues) {
			t.Fatalf("clean install did not use exact controller-first overrides: %#v", values)
		}
		if upgradeCalls == 2 || upgradeCalls == 4 || upgradeCalls == 6 {
			if len(values) != 1 {
				t.Fatalf("final baseline activation used staging overrides: %#v", values)
			}
		}
		if upgradeCalls == 3 || upgradeCalls == 5 {
			holdValues, err := nodeAgentTransitionHoldValues(activeTransitionPlan)
			if err != nil || len(values) != 2 || values[1] != holdValues {
				t.Fatalf("release transition did not retain the exact prior node agent: %#v %v", values, err)
			}
		}
		if upgradeCalls >= 2 {
			revision++
			if revision == 1 {
				revision = 2
			}
			if upgradeCalls == 3 || upgradeCalls == 5 {
				seedStagedRelease(t, clients, stageSource, activeManifest, activeTransitionPlan.PlanID, plan.Namespace, plan.Release, revision, testCRDs)
			} else {
				seedInstalledRelease(t, clients, activeManifest, plan.Namespace, plan.Release, revision, testCRDs)
			}
		}
		return nil, nil
	}
	if err := ApplyInstallPlan(context.Background(), clients, runner, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if upgradeCalls != 2 {
		t.Fatalf("Helm upgrade was called %d times", upgradeCalls)
	}
	namespace, err := clients.Kubernetes.CoreV1().Namespaces().Get(context.Background(), plan.Namespace, metav1.GetOptions{})
	if err != nil || namespace.Labels["pod-security.kubernetes.io/enforce"] != "privileged" {
		t.Fatalf("reviewed system namespace missing: %#v %v", namespace, err)
	}
	tlsSecret, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(context.Background(), plan.Release+"-observation-tls", metav1.GetOptions{})
	if err != nil || len(tlsSecret.Data["tls.key"]) == 0 {
		t.Fatalf("in-memory TLS identity missing: %v", err)
	}
	initialTLSUID := tlsSecret.UID
	initialClass, err := clients.Dynamic.Resource(gatewayClassGVR).Get(context.Background(), "gluetun.waycloak.io", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	forward := manifest
	forward.Version = "v1.0.0-beta.2"
	forward.ManifestDigest, err = forward.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	forwardSource, err := ObserveInstalledRelease(context.Background(), clients, plan.Namespace, plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	forwardPlan, err := BuildInstallPlan(forward, plan.Namespace, plan.Release, "", report, forwardSource, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	stageSource = forwardSource
	activeManifest = forward
	activeTransitionPlan = forwardPlan
	if err := ApplyInstallPlan(context.Background(), clients, runner, forwardPlan, forwardPlan.PlanID); err != nil {
		t.Fatal(err)
	}
	if upgradeCalls != 4 {
		t.Fatalf("exact transition called Helm %d total times, want 4", upgradeCalls)
	}
	tlsSecret, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(context.Background(), plan.Release+"-observation-tls", metav1.GetOptions{})
	if err != nil || tlsSecret.UID != initialTLSUID {
		t.Fatalf("ordinary transition replaced serving identity: %#v %v", tlsSecret, err)
	}
	forwardClass, err := clients.Dynamic.Resource(gatewayClassGVR).Get(context.Background(), "gluetun.waycloak.io", metav1.GetOptions{})
	if err != nil || forwardClass.GetUID() == initialClass.GetUID() {
		t.Fatalf("forward transition did not replace immutable class identity: %#v %v", forwardClass, err)
	}
	rollbackSource, err := ObserveInstalledRelease(context.Background(), clients, plan.Namespace, plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	rollbackPlan, err := BuildInstallPlan(manifest, plan.Namespace, plan.Release, "", report, rollbackSource, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	stageSource = rollbackSource
	activeManifest = manifest
	activeTransitionPlan = rollbackPlan
	if err := ApplyInstallPlan(context.Background(), clients, runner, rollbackPlan, rollbackPlan.PlanID); err != nil {
		t.Fatal(err)
	}
	if upgradeCalls != 6 {
		t.Fatalf("exact rollback called Helm %d total times, want 6", upgradeCalls)
	}
	rollbackClass, err := clients.Dynamic.Resource(gatewayClassGVR).Get(context.Background(), "gluetun.waycloak.io", metav1.GetOptions{})
	if err != nil || rollbackClass.GetUID() == forwardClass.GetUID() {
		t.Fatalf("rollback did not replace immutable class identity: %#v %v", rollbackClass, err)
	}
	tamperSource, err := ObserveInstalledRelease(context.Background(), clients, plan.Namespace, plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	tamperPlan, err := BuildInstallPlan(forward, plan.Namespace, plan.Release, "", report, tamperSource, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	tlsSecret.Data["tls.crt"] = []byte("tampered")
	if _, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Update(context.Background(), tlsSecret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyInstallPlan(context.Background(), clients, runner, tamperPlan, tamperPlan.PlanID); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("tampered install identity accepted: %v", err)
	}
	if upgradeCalls != 6 {
		t.Fatal("tampered source reached Helm mutation")
	}
}

func TestInstallApplyResumesOnlyJournalBoundExactCheckpoints(t *testing.T) {
	ctx := context.Background()
	clients := supportedClients(t)
	report, err := Preflight(ctx, clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "waycloak-system", Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	crdObjects, crds, crdBundle := testInstallCRDBundle(t)
	sourceManifest := releaseManifest()
	seedInstalledRelease(t, clients, sourceManifest, "waycloak-system", "waycloak", 1, crdObjects)
	source, err := ObserveInstalledRelease(ctx, clients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	target := sourceManifest
	target.Version = "v1.0.0-beta.2"
	target.ManifestDigest, err = target.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildInstallPlan(target, "waycloak-system", "waycloak", "", report, source, crds, nil)
	if err != nil {
		t.Fatal(err)
	}

	revision := int64(1)
	upgradeCalls := 0
	interruptStage := true
	runner := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name != "helm" {
			t.Fatalf("unexpected command %s %#v", name, arguments)
		}
		if len(arguments) >= 2 && arguments[0] == "show" && arguments[1] == "crds" {
			return crdBundle, nil
		}
		upgradeCalls++
		revision++
		staging := false
		for index, argument := range arguments {
			if argument != "--values" || index+1 >= len(arguments) {
				continue
			}
			data, readErr := os.ReadFile(arguments[index+1])
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(data), "releaseIdentity:\n    version: "+strconv.Quote(source.Version)) {
				staging = true
			}
		}
		if staging {
			seedStagedRelease(t, clients, source, target, plan.PlanID, plan.Namespace, plan.Release, revision, crdObjects)
			if interruptStage {
				interruptStage = false
				return nil, context.Canceled
			}
			return nil, nil
		}
		seedInstalledRelease(t, clients, target, plan.Namespace, plan.Release, revision, crdObjects)
		return nil, nil
	}

	if err = ApplyInstallPlan(ctx, clients, runner, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "staging failed") {
		t.Fatalf("simulated client interruption was not retained as a failed apply: %v", err)
	}
	if upgradeCalls != 1 {
		t.Fatalf("interrupted apply executed %d Helm upgrades, want staging only", upgradeCalls)
	}
	active, journal, found, err := loadInstallTransitionJournal(ctx, clients, plan.Namespace, plan.Release)
	if err != nil || !found || active.PlanID != plan.PlanID || journal.Immutable == nil || !*journal.Immutable {
		t.Fatalf("interrupted transition lost its exact immutable journal: %#v %#v %v", active, journal, err)
	}
	if strings.Contains(journal.Data[installTransitionPlanData], "tls.key") || strings.Contains(journal.Data[installTransitionPlanData], "PRIVATE KEY") {
		t.Fatal("transition journal included credential material")
	}
	tamperedJournal := journal.DeepCopy()
	tamperedJournal.Annotations[installReleaseOwnerKey] = "foreign-release"
	if _, err = clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Update(ctx, tamperedJournal, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = ApplyInstallPlan(ctx, clients, runner, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "foreign or malformed") {
		t.Fatalf("foreign transition journal was accepted: %v", err)
	}
	if upgradeCalls != 1 {
		t.Fatal("foreign journal reached Helm mutation")
	}
	journal, err = clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Get(ctx, journal.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	journal.Annotations[installReleaseOwnerKey] = plan.Release
	if journal, err = clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Update(ctx, journal, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	recovered, found, err := recoverInstallTransitionPlan(ctx, clients, plan.Namespace, plan.Release, report, target, crds)
	if err != nil || !found || !reflect.DeepEqual(recovered, plan) {
		t.Fatalf("install plan did not recover the exact active transaction: %#v %v", recovered, err)
	}
	otherTarget := target
	otherTarget.Version = "v1.0.0-beta.3"
	otherTarget.ManifestDigest, err = otherTarget.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err = recoverInstallTransitionPlan(ctx, clients, plan.Namespace, plan.Release, report, otherTarget, crds); err == nil || !found || !strings.Contains(err.Error(), "different exact release") {
		t.Fatalf("active transaction permitted a different target: found=%t err=%v", found, err)
	}

	agent, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, chartFullname(plan.Release)+"-node-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agent.Spec.Template.Spec.Containers[0].Image = target.Images["waycloak-node-agent"].Repository + "@sha256:" + strings.Repeat("f", 64)
	if _, err = clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Update(ctx, agent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = ApplyInstallPlan(ctx, clients, runner, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "exact transition checkpoint") {
		t.Fatalf("ambiguous staged node identity was accepted: %v", err)
	}
	if upgradeCalls != 1 {
		t.Fatal("ambiguous checkpoint reached Helm mutation")
	}
	agent.Spec.Template.Spec.Containers[0].Image = source.Images["waycloak-node-agent"]
	if _, err = clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Update(ctx, agent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	savedJournal := journal.DeepCopy()
	if err = clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Delete(ctx, journal.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = ApplyInstallPlan(ctx, clients, runner, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "no matching transition journal") {
		t.Fatalf("staged state without its journal was accepted: %v", err)
	}
	if upgradeCalls != 1 {
		t.Fatal("journal-less checkpoint reached Helm mutation")
	}
	savedJournal.ResourceVersion = ""
	savedJournal.UID = ""
	if _, err = clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Create(ctx, savedJournal, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = ApplyInstallPlan(ctx, clients, runner, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if upgradeCalls != 2 {
		t.Fatalf("staged recovery repeated work: Helm upgrades=%d, want stage plus activation", upgradeCalls)
	}
	if _, _, found, err = loadInstallTransitionJournal(ctx, clients, plan.Namespace, plan.Release); err != nil || found {
		t.Fatalf("completed transition retained its lifecycle journal: found=%t err=%v", found, err)
	}
	if err = ApplyInstallPlan(ctx, clients, runner, plan, plan.PlanID); err != nil {
		t.Fatalf("completed exact plan was not retry-idempotent: %v", err)
	}
	if upgradeCalls != 2 {
		t.Fatal("completed retry reached Helm mutation")
	}
}

func TestInstallApplyResumesAfterExactClassWithdrawal(t *testing.T) {
	ctx := context.Background()
	clients := supportedClients(t)
	report, err := Preflight(ctx, clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "waycloak-system", Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}
	crdObjects, crds, crdBundle := testInstallCRDBundle(t)
	sourceManifest := releaseManifest()
	seedInstalledRelease(t, clients, sourceManifest, "waycloak-system", "waycloak", 1, crdObjects)
	source, err := ObserveInstalledRelease(ctx, clients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	target := sourceManifest
	target.Version = "v1.0.0-beta.2"
	target.ManifestDigest, err = target.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildInstallPlan(target, "waycloak-system", "waycloak", "", report, source, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ensureInstallTransitionJournal(ctx, clients, plan); err != nil {
		t.Fatal(err)
	}
	uid := k8stypes.UID(source.GatewayClassUID)
	if err = clients.Dynamic.Resource(gatewayClassGVR).Delete(ctx, "gluetun.waycloak.io", metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		t.Fatal(err)
	}

	revision := int64(1)
	upgradeCalls := 0
	runner := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if len(arguments) >= 2 && arguments[0] == "show" && arguments[1] == "crds" {
			return crdBundle, nil
		}
		if name != "helm" {
			t.Fatalf("unexpected command %s", name)
		}
		upgradeCalls++
		revision++
		if upgradeCalls == 1 {
			seedStagedRelease(t, clients, source, target, plan.PlanID, plan.Namespace, plan.Release, revision, crdObjects)
		} else {
			seedInstalledRelease(t, clients, target, plan.Namespace, plan.Release, revision, crdObjects)
		}
		return nil, nil
	}
	if err = ApplyInstallPlan(ctx, clients, runner, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if upgradeCalls != 2 {
		t.Fatalf("class-withdrawn recovery ran %d Helm upgrades, want stage and activation", upgradeCalls)
	}
}

func TestTransitionClassWithdrawalWaitsForObservedAttachmentDeny(t *testing.T) {
	ctx := context.Background()
	clients := supportedClients(t)
	report, err := Preflight(ctx, clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "waycloak-system", Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}
	crdObjects, crds, _ := testInstallCRDBundle(t)
	sourceManifest := releaseManifest()
	seedInstalledRelease(t, clients, sourceManifest, "waycloak-system", "waycloak", 1, crdObjects)
	source, err := ObserveInstalledRelease(ctx, clients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	target := sourceManifest
	target.Version = "v1.0.0-beta.2"
	target.Images["waycloak-node-agent"] = Artifact{Repository: target.Images["waycloak-node-agent"].Repository, Digest: "sha256:" + strings.Repeat("a", 64)}
	target.ManifestDigest, err = target.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildInstallPlan(target, "waycloak-system", "waycloak", "", report, source, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ensureInstallTransitionJournal(ctx, clients, plan); err != nil {
		t.Fatal(err)
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.waycloak.io/v1beta1", "kind": "VPNWorkloadBinding",
		"metadata": map[string]any{"name": "binding", "namespace": "media", "generation": int64(1)},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "True"},
			map[string]any{"type": "NodeReady", "status": "True"},
		}},
	}}
	if _, err = clients.Dynamic.Resource(transitionBindingGVR).Namespace("media").Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	err = ensureTransitionQuiescence(bounded, clients, plan)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "acknowledge transition deny") {
		t.Fatalf("ready attachment allowed transition quiescence: %v", err)
	}
	heldAgent, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, chartFullname(plan.Release)+"-node-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	heldContainer, err := requiredContainer(heldAgent.Spec.Template.Spec.Containers, "node-agent")
	if err != nil {
		t.Fatal(err)
	}
	for prefix, expected := range map[string]string{
		"--cni-release-version=":         source.Version,
		"--cni-release-manifest-digest=": source.ManifestDigest,
	} {
		actual, argumentErr := optionalSingularArgument(heldContainer.Args, prefix)
		if argumentErr != nil || actual != expected {
			t.Fatalf("transition hold CNI identity %s = %q, want %q: %v", prefix, actual, expected, argumentErr)
		}
	}
	if _, err = clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, "gluetun.waycloak.io", metav1.GetOptions{}); err != nil {
		t.Fatalf("class was withdrawn before attachment deny acknowledgement: %v", err)
	}
	current, err := clients.Dynamic.Resource(transitionBindingGVR).Namespace("media").Get(ctx, "binding", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(current.Object, int64(1), "status", "observedGeneration"); err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(current.Object, int64(0), "status", "appliedGeneration"); err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedMap(current.Object, map[string]any{
		"nodeName": "node", "instanceID": plan.PlanID,
	}, "status", "agent"); err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedSlice(current.Object, []any{
		map[string]any{"type": "Programmed", "status": "False"},
		map[string]any{"type": "Ready", "status": "False"},
		map[string]any{"type": "NodeReady", "status": "False"},
	}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if _, err = clients.Dynamic.Resource(transitionBindingGVR).Namespace("media").Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = ensureTransitionQuiescence(ctx, clients, plan); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := observeInstallTransitionCheckpoint(ctx, clients, plan, crds)
	if err != nil || checkpoint != installCheckpointQuiesced {
		t.Fatalf("restarted apply did not recover the exact quiesced checkpoint: checkpoint=%q err=%v", checkpoint, err)
	}
	if err = replaceGatewayClassForTransition(ctx, clients, plan); err != nil {
		t.Fatal(err)
	}
	if _, err = clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, "gluetun.waycloak.io", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("acknowledged transition did not withdraw the exact class: %v", err)
	}
}

func TestInstallApplyRejectsPreflightDriftBeforeMutation(t *testing.T) {
	clients := supportedClients(t)
	report, err := Preflight(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	source, crds := absentInstallInputs(t)
	plan, err := BuildInstallPlan(releaseManifest(), "waycloak-system", "waycloak", "", report, source, crds, nil)
	if err != nil {
		t.Fatal(err)
	}
	node, err := clients.Kubernetes.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.NodeInfo.KernelVersion = "6.9.0"
	if _, err = clients.Kubernetes.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	called := false
	runner := func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	err = ApplyInstallPlan(context.Background(), clients, runner, plan, plan.PlanID)
	if err == nil || !strings.Contains(err.Error(), "preflight observation changed") || called {
		t.Fatalf("drift did not fail before Helm: called=%t err=%v", called, err)
	}
	if _, err = clients.Kubernetes.CoreV1().Namespaces().Get(context.Background(), plan.Namespace, metav1.GetOptions{}); !errorsIsNotFound(err) {
		t.Fatalf("drift refusal mutated the cluster: %v", err)
	}
}

func TestInstallObservationSecretsRecoverOnlyFromServingIdentity(t *testing.T) {
	clients := supportedClients(t)
	ctx := context.Background()
	if _, err := clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "waycloak-system"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	planID := "sha256:" + strings.Repeat("a", 64)
	ca, serving, err := ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", planID)
	if err != nil {
		t.Fatal(err)
	}
	originalCA := append([]byte(nil), ca.Data["ca.crt"]...)
	originalServingUID := serving.UID
	if err = clients.Kubernetes.CoreV1().Secrets("waycloak-system").Delete(ctx, ca.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	ca, serving, err = ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", planID)
	if err != nil || !bytes.Equal(ca.Data["ca.crt"], originalCA) || serving.UID != originalServingUID {
		t.Fatalf("partial retry did not reconstruct public CA from stable serving identity: %#v %#v %v", ca, serving, err)
	}
	if err = clients.Kubernetes.CoreV1().Secrets("waycloak-system").Delete(ctx, serving.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", planID); err == nil || !strings.Contains(err.Error(), "explicit certificate-rotation") {
		t.Fatalf("missing serving private key was silently regenerated: %v", err)
	}
}

func TestChartCRDIdentityAndStorageChangeGate(t *testing.T) {
	_, identities, bundle := testInstallCRDBundle(t)
	chart := releaseManifest().Chart
	runner := func(context.Context, string, ...string) ([]byte, error) { return bundle, nil }
	observed, err := ChartCRDIdentities(context.Background(), runner, chart)
	if err != nil || !reflect.DeepEqual(observed, identities) {
		t.Fatalf("exact chart CRD inventory was not canonical: %#v %v", observed, err)
	}
	source, err := finalizeInstalledReleaseObservation(InstalledReleaseObservation{State: installStateAbsent, CRDIdentities: identities})
	if err != nil {
		t.Fatal(err)
	}
	changed := copyStringMap(identities)
	changed[stateCRDNames[0]] = "sha256:" + strings.Repeat("f", 64)
	if err = validateInstallCRDTransition(source, changed); err == nil || !strings.Contains(err.Error(), "storage migration") {
		t.Fatalf("unplanned CRD transition was accepted: %v", err)
	}
	extra := append(append([]byte(nil), bundle...), []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"unexpected"}}`)...)
	if _, err = ChartCRDIdentities(context.Background(), func(context.Context, string, ...string) ([]byte, error) { return extra, nil }, chart); err == nil || !strings.Contains(err.Error(), "unexpected CRD") {
		t.Fatalf("unexpected chart document was accepted: %v", err)
	}
}

func TestGatewayRecipeContainsReferencesButNoCredentialValues(t *testing.T) {
	value, err := RenderGatewayRecipe(GatewayRecipe{Namespace: "media", Name: "private", ClassName: "gluetun.waycloak.io", ConfigMapName: "proton", SecretName: "proton-credentials", Provider: "protonvpn", Protocol: "openvpn", OverlayCIDR: "100.96.0.0/16", AllowDisruptiveVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(value, "credentialRefs:") || !strings.Contains(value, "name: proton-credentials") || !strings.Contains(value, "verify.waycloak.io/dedicated") || strings.Contains(strings.ToLower(value), "password:") || strings.Contains(strings.ToLower(value), "username:") {
		t.Fatalf("unsafe recipe:\n%s", value)
	}
}

func TestTunnelLossTargetsOnlyExactOwnedGatewayPod(t *testing.T) {
	controller := true
	gateway := &unstructured.Unstructured{}
	gateway.SetAPIVersion("networking.waycloak.io/v1beta1")
	gateway.SetKind("VPNGateway")
	gateway.SetName("private")
	gateway.SetNamespace("media")
	gateway.SetUID("gateway-uid")
	statefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "media", UID: "statefulset-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: gateway.GetAPIVersion(), Kind: gateway.GetKind(), Name: gateway.GetName(), UID: gateway.GetUID(), Controller: &controller}}}}
	exact := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "exact", Namespace: "media", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: statefulSet.Name, UID: statefulSet.UID, Controller: &controller}}}}
	foreign := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "media", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "other", UID: "other-uid", Controller: &controller}}}}
	clients := supportedClients(t)
	clients.Kubernetes = kubernetesfake.NewSimpleClientset(statefulSet, exact, foreign)
	if err := deleteExactGatewayPod(context.Background(), clients, "media", gateway); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Kubernetes.CoreV1().Pods("media").Get(context.Background(), "exact", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("exact Pod was not deleted: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().Pods("media").Get(context.Background(), "foreign", metav1.GetOptions{}); err != nil {
		t.Fatalf("foreign Pod was touched: %v", err)
	}
}

func TestVerifyConfirmationBindsProbeEndpointAndPublicCA(t *testing.T) {
	base := verifyConfirmation("media", "private", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "observer-ca")
	for name, value := range map[string]string{
		"namespace": verifyConfirmation("other", "private", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "observer-ca"),
		"gateway":   verifyConfirmation("media", "other", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "observer-ca"),
		"image":     verifyConfirmation("media", "private", "registry.invalid/probe@sha256:"+strings.Repeat("b", 64), "https://198.18.0.1:8443/ip", "observer-ca"),
		"url":       verifyConfirmation("media", "private", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.2:8443/ip", "observer-ca"),
		"ca":        verifyConfirmation("media", "private", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "other-ca"),
	} {
		if value == base {
			t.Fatalf("%s was not bound into the disruptive verification identity", name)
		}
	}
}

func TestWaitForAbsenceRequiresObservedDeletion(t *testing.T) {
	calls := 0
	err := waitForAbsence(context.Background(), "test object", func(context.Context) error {
		calls++
		if calls < 2 {
			return nil
		}
		return apierrors.NewNotFound(schema.GroupResource{Resource: "tests"}, "object")
	})
	if err != nil || calls != 2 {
		t.Fatalf("observed deletion wait = %d calls, %v", calls, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = waitForAbsence(ctx, "stuck object", func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "cleanup did not complete") {
		t.Fatalf("unobserved cleanup reported complete: %v", err)
	}
}

func TestProbePodUsesHTTPSObserverAndPublicCAWithoutCredentials(t *testing.T) {
	pod := probePod("probe", "media", "registry.invalid/probe@sha256:"+strings.Repeat("a", 64), "https://198.18.0.1:8443/ip", "observer-ca", map[string]string{"verify.waycloak.io/run": "test"}, nil)
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || len(pod.Spec.Containers) != 1 {
		t.Fatalf("probe received a Kubernetes credential or unexpected containers: %#v", pod.Spec)
	}
	container := pod.Spec.Containers[0]
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].ConfigMap == nil || pod.Spec.Volumes[0].ConfigMap.Name != "observer-ca" || len(container.VolumeMounts) != 1 || !container.VolumeMounts[0].ReadOnly {
		t.Fatalf("probe public CA is not a read-only ConfigMap mount: %#v %#v", pod.Spec.Volumes, container.VolumeMounts)
	}
	if container.SecurityContext == nil || container.SecurityContext.RunAsNonRoot == nil || !*container.SecurityContext.RunAsNonRoot || len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("probe security context regressed: %#v", container.SecurityContext)
	}
}

func TestSupportBundleIsDeterministicAndOmitsCanaries(t *testing.T) {
	clients := supportedClients(t)
	canary := "CANARY-SECRET-private.example.invalid"
	_, _ = clients.Kubernetes.CoreV1().Secrets("default").Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "canary", Namespace: "default"}, Data: map[string][]byte{"password": []byte(canary)}}, metav1.CreateOptions{})
	_, _ = clients.Kubernetes.CoreV1().Events("default").Create(context.Background(), &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "event", Namespace: "default"}, InvolvedObject: corev1.ObjectReference{APIVersion: "networking.waycloak.io/v1beta1", Kind: "VPNGateway", Name: "gateway"}, Reason: "Unavailable", Type: "Warning", Message: canary}, metav1.CreateOptions{})
	first, err := SupportBundle(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	second, err := SupportBundle(context.Background(), clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("support bundle is not deterministic for unchanged state")
	}
	contents := untar(t, first)
	if bytes.Contains(contents, []byte(canary)) {
		t.Fatal("support bundle leaked a canary")
	}
	for _, wanted := range []string{"manifest.json", "preflight.json", "doctor.json", "events.json", "Secret objects and data"} {
		if !bytes.Contains(contents, []byte(wanted)) {
			t.Fatalf("bundle lacks %q", wanted)
		}
	}
}

func supportedClients(t *testing.T) *Clients {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}, Spec: corev1.NodeSpec{PodCIDRs: []string{"10.42.0.0/24"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux", KernelVersion: "6.8.0", ContainerRuntimeVersion: "containerd://2.1.0"}}}
	dns := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "kube-dns"}}, Spec: corev1.ServiceSpec{ClusterIP: "10.43.0.10"}}
	coreDNS := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"}, Data: map[string]string{"Corefile": ".:53 {\n  kubernetes cluster.local in-addr.arpa ip6.arpa {\n    pods insecure\n  }\n}\n"}}
	kube := kubernetesfake.NewSimpleClientset(node, dns, coreDNS,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "test-cluster-uid"}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "kube-system"}})
	kube.Fake.PrependReactor("create", "secrets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		create := action.(clienttesting.CreateAction)
		secret := create.GetObject().(*corev1.Secret)
		if secret.UID == "" {
			secret.UID = k8stypes.UID("test-secret-" + secret.Namespace + "-" + secret.Name)
		}
		return false, nil, nil
	})
	discovery := &fake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{{GroupVersion: "admissionregistration.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "validatingadmissionpolicies"}, {Name: "mutatingadmissionpolicies"}}}}}, FakedServerVersion: &version.Info{Major: "1", Minor: "36", GitVersion: "v1.36.1+k3s1"}}
	listKinds := map[schema.GroupVersionResource]string{}
	for _, resource := range doctorResources {
		listKinds[resource.GVR] = resource.Kind + "List"
	}
	return &Clients{
		Kubernetes: kube, APIExtensions: apiextensionsfake.NewSimpleClientset(),
		Dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds), Discovery: discovery,
		ClusterServerFingerprint: "sha256:" + strings.Repeat("a", 64), ClusterTrustFingerprint: "sha256:" + strings.Repeat("b", 64),
	}
}

func absentInstallInputs(t *testing.T) (InstalledReleaseObservation, map[string]string) {
	t.Helper()
	source, err := finalizeInstalledReleaseObservation(InstalledReleaseObservation{State: installStateAbsent, CRDIdentities: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	_, identities, _ := testInstallCRDBundle(t)
	return source, identities
}

func testInstallCRDBundle(t *testing.T) ([]*apiextensionsv1.CustomResourceDefinition, map[string]string, []byte) {
	t.Helper()
	identities := make(map[string]string, len(stateCRDNames))
	crds := make([]*apiextensionsv1.CustomResourceDefinition, 0, len(stateCRDNames))
	var bundle bytes.Buffer
	for _, name := range stateCRDNames {
		plural := strings.TrimSuffix(name, ".networking.waycloak.io")
		kind := map[string]string{
			"portforwardleases": "PortForwardLease", "vpnegressroutes": "VPNEgressRoute", "vpngatewayclasses": "VPNGatewayClass",
			"vpngateways": "VPNGateway", "vpnworkloadbindings": "VPNWorkloadBinding", "workloadadapters": "WorkloadAdapter",
		}[plural]
		scope := apiextensionsv1.NamespaceScoped
		if plural == "vpngatewayclasses" {
			scope = apiextensionsv1.ClusterScoped
		}
		crd := &apiextensionsv1.CustomResourceDefinition{
			TypeMeta:   metav1.TypeMeta{APIVersion: apiextensionsv1.SchemeGroupVersion.String(), Kind: "CustomResourceDefinition"},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "networking.waycloak.io", Scope: scope,
				Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: plural, Kind: kind},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1beta1", Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"}}}},
			},
		}
		digest, err := installCRDSpecDigest(crd.Spec)
		if err != nil {
			t.Fatal(err)
		}
		identities[name] = digest
		crds = append(crds, crd)
		data, err := json.Marshal(crd)
		if err != nil {
			t.Fatal(err)
		}
		bundle.Write(data)
		bundle.WriteString("\n---\n")
	}
	return crds, identities, bundle.Bytes()
}

func seedInstalledRelease(t *testing.T, clients *Clients, manifest ReleaseManifest, namespace, release string, revision int64, crds []*apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	ctx := context.Background()
	for _, crd := range crds {
		if _, err := clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			if _, err = clients.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd.DeepCopy(), metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	releaseSecrets, err := clients.Kubernetes.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{"owner": "helm", "name": release}.AsSelector().String()})
	if err != nil {
		t.Fatal(err)
	}
	for index := range releaseSecrets.Items {
		secret := releaseSecrets.Items[index].DeepCopy()
		secret.Labels["status"] = "superseded"
		if _, err = clients.Kubernetes.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	helmSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "sh.helm.release.v1." + release + ".v" + strconv.FormatInt(revision, 10), Namespace: namespace,
		Labels: map[string]string{"owner": "helm", "name": release, "status": "deployed", "version": strconv.FormatInt(revision, 10)},
	}, Type: corev1.SecretType("helm.sh/release.v1"), Data: map[string][]byte{"release": []byte("opaque-test-release-record")}}
	if _, err = clients.Kubernetes.CoreV1().Secrets(namespace).Create(ctx, helmSecret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	image := func(name string) string { return manifest.Images[name].Repository + "@" + manifest.Images[name].Digest }
	fullname := chartFullname(release)
	controller := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: fullname + "-controller", Namespace: namespace}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "controller", Image: image("replacement-controller"), Args: []string{
			"--release-version=" + manifest.Version, "--release-manifest-digest=" + manifest.ManifestDigest,
			"--gateway-engine-image=" + image("gluetun"), "--gateway-agent-image=" + image("waycloak-gateway-agent"),
		},
	}}}}}}
	if current, getErr := clients.Kubernetes.AppsV1().Deployments(namespace).Get(ctx, controller.Name, metav1.GetOptions{}); getErr == nil {
		controller.ResourceVersion = current.ResourceVersion
		if _, err = clients.Kubernetes.AppsV1().Deployments(namespace).Update(ctx, controller, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else if apierrors.IsNotFound(getErr) {
		if _, err = clients.Kubernetes.AppsV1().Deployments(namespace).Create(ctx, controller, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal(getErr)
	}
	cni := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fullname + "-cni-installer", Namespace: namespace}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "install", Image: image("waycloak-cni"), Args: []string{"install", manifest.Version, manifest.ManifestDigest}}},
		Containers:     []corev1.Container{{Name: "receipt-holder", Image: image("pause")}},
	}}}}
	upsertTestDaemonSet(t, clients, cni)
	agent := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fullname + "-node-agent", Namespace: namespace, Generation: 1}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "node-agent", Image: image("waycloak-node-agent"), Args: []string{"--release-version=" + manifest.Version, "--release-manifest-digest=" + manifest.ManifestDigest},
	}}}}}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1, UpdatedNumberScheduled: 1, NumberReady: 1, NumberAvailable: 1}}
	upsertTestDaemonSet(t, clients, agent)
	class := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.waycloak.io/v1beta1", "kind": "VPNGatewayClass",
		"metadata": map[string]any{"name": "gluetun.waycloak.io"},
		"spec":     map[string]any{"releaseIdentity": map[string]any{"version": manifest.Version, "manifestDigest": manifest.ManifestDigest}},
	}}
	if current, getErr := clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, class.GetName(), metav1.GetOptions{}); getErr == nil {
		class.SetUID(current.GetUID())
		class.SetResourceVersion(current.GetResourceVersion())
		class.SetGeneration(current.GetGeneration() + 1)
		if _, err = clients.Dynamic.Resource(gatewayClassGVR).Update(ctx, class, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else if apierrors.IsNotFound(getErr) {
		class.SetUID(k8stypes.UID("test-gateway-class-" + strconv.FormatInt(revision, 10)))
		class.SetGeneration(1)
		if _, err = clients.Dynamic.Resource(gatewayClassGVR).Create(ctx, class, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal(getErr)
	}
}

func seedStagedRelease(t *testing.T, clients *Clients, source InstalledReleaseObservation, target ReleaseManifest, planID, namespace, release string, revision int64, crds []*apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	seedInstalledRelease(t, clients, target, namespace, release, revision, crds)
	controller, err := clients.Kubernetes.AppsV1().Deployments(namespace).Get(context.Background(), chartFullname(release)+"-controller", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	controller.Spec.Template.Spec.Containers[0].Args = append(controller.Spec.Template.Spec.Containers[0].Args,
		"--transition-plan-id="+planID,
		"--transition-source-version="+source.Version,
		"--transition-source-manifest-digest="+source.ManifestDigest,
	)
	if _, err = clients.Kubernetes.AppsV1().Deployments(namespace).Update(context.Background(), controller, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	agent, err := clients.Kubernetes.AppsV1().DaemonSets(namespace).Get(context.Background(), chartFullname(release)+"-node-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agent.Spec.Template.Spec.Containers[0].Args = []string{"--release-version=" + source.Version, "--release-manifest-digest=" + source.ManifestDigest, "--observation-capability-hold=true", "--observation-capability-hold-id=" + planID}
	if agent.Spec.Template.Annotations == nil {
		agent.Spec.Template.Annotations = map[string]string{}
	}
	agent.Spec.Template.Annotations[installTransitionPlanKey] = planID
	if _, err = clients.Kubernetes.AppsV1().DaemonSets(namespace).Update(context.Background(), agent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func upsertTestDaemonSet(t *testing.T, clients *Clients, daemonSet *appsv1.DaemonSet) {
	t.Helper()
	ctx := context.Background()
	current, err := clients.Kubernetes.AppsV1().DaemonSets(daemonSet.Namespace).Get(ctx, daemonSet.Name, metav1.GetOptions{})
	if err == nil {
		daemonSet.ResourceVersion = current.ResourceVersion
		if _, err = clients.Kubernetes.AppsV1().DaemonSets(daemonSet.Namespace).Update(ctx, daemonSet, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if _, err = clients.Kubernetes.AppsV1().DaemonSets(daemonSet.Namespace).Create(ctx, daemonSet, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func releaseManifest() ReleaseManifest {
	digest := func(char string) string { return "sha256:" + strings.Repeat(char, 64) }
	manifest := ReleaseManifest{APIVersion: "release.waycloak.io/v1", Version: "v1.0.0-beta.1", Chart: Artifact{Repository: "oci://ghcr.io/amoenus/charts/waycloak", Digest: digest("b")}, Images: map[string]Artifact{"replacement-controller": {Repository: "ghcr.io/amoenus/waycloak-replacement-controller", Digest: digest("c")}, "waycloak-cni": {Repository: "ghcr.io/amoenus/waycloak-cni", Digest: digest("d")}, "waycloak-node-agent": {Repository: "ghcr.io/amoenus/waycloak-node-agent", Digest: digest("e")}, "waycloak-gateway-agent": {Repository: "ghcr.io/amoenus/waycloak-gateway-agent", Digest: digest("f")}, "waycloak-gateway-runtime": {Repository: "ghcr.io/amoenus/waycloak-gateway-runtime", Digest: digest("7")}, "waycloak-qbittorrent-adapter": {Repository: "ghcr.io/amoenus/waycloak-qbittorrent-adapter", Digest: digest("8")}, "gluetun": {Repository: "docker.io/qmcgaw/gluetun", Digest: digest("1")}, "coredns": {Repository: "docker.io/coredns/coredns", Digest: digest("9")}, "pause": {Repository: "registry.k8s.io/pause", Digest: digest("2")}}, Profiles: []string{"networking.waycloak.io/Core-v1"}, SupportMatrix: CertifiedSupportMatrix()}
	manifest.ManifestDigest, _ = manifest.IdentityDigest()
	return manifest
}

func portForwardReleaseManifest() ReleaseManifest {
	return releaseManifest()
}

func portForwardControllerIdentity(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	now := time.Now().UTC()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Waycloak port-forward test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := url.Parse(portForwardControllerSPIFFEIdentity)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Waycloak port-forward controller"}, URIs: []*url.URL{identity}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caTemplate, clientPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func checkStatus(report PreflightReport, name string) string {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if strings.Contains(value, wanted) {
			return true
		}
	}
	return false
}
func errorsIsNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}
func untar(t *testing.T, data []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(reader)
	var output bytes.Buffer
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		output.WriteString(header.Name)
		content, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(content)
	}
	return output.Bytes()
}

func TestLoadReleaseManifestRejectsUnknownFields(t *testing.T) {
	manifest := releaseManifest()
	data, _ := json.Marshal(manifest)
	data = bytes.Replace(data, []byte(`{"apiVersion"`), []byte(`{"unknown":true,"apiVersion"`), 1)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadReleaseManifest(path); err == nil {
		t.Fatal("unknown manifest field was accepted")
	}
}
