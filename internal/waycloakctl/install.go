// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const controllerFirstBootstrapValues = `cniInstaller:
  enabled: false
nodeAgent:
  enabled: false
defaultGatewayClass:
  enabled: false
`

func LoadInstallPlan(path string) (InstallPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return InstallPlan{}, err
	}
	return decodeInstallPlan(data)
}

func decodeInstallPlan(data []byte) (InstallPlan, error) {
	if len(data) > 2<<20 {
		return InstallPlan{}, errors.New("install plan exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan InstallPlan
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return plan, errors.New("install plan contains trailing JSON")
	}
	if err := plan.validate(); err != nil {
		return plan, err
	}
	return plan, nil
}

func (plan InstallPlan) validate() error {
	if plan.APIVersion != OutputAPIVersion || plan.Kind != "InstallPlan" || !validDigest(plan.PlanID) || !validDigest(plan.Manifest) || !validDigest(plan.PreflightDigest) || plan.InstallSequence != failClosedLifecycleSequence ||
		plan.Namespace == "" || plan.Release == "" || plan.Values == "" || plan.OverlayCIDR == "" || plan.NodeArchitecture != "amd64" && plan.NodeArchitecture != "arm64" || plan.Chart.Repository == "" || !validDigest(plan.Chart.Digest) {
		return errors.New("install plan identity is incomplete")
	}
	if err := plan.Target.Validate(); err != nil || plan.Target.ManifestDigest != plan.Manifest || plan.Target.Chart != plan.Chart {
		return errors.New("install plan target release identity is inconsistent")
	}
	if plan.PortForwarding == nil {
		if strings.Contains(plan.Values, "portForwarding:\n") {
			return errors.New("install plan contains an unbound port-forward configuration")
		}
	} else {
		if err := plan.PortForwarding.validate(); err != nil {
			return errors.New("install plan port-forward identity is inconsistent")
		}
		runtime, ok := plan.Target.Images["waycloak-gateway-runtime"]
		expected := fmt.Sprintf("portForwarding:\n  enabled: true\n  controllerTLSSecret: %q\n  gatewayRuntime:\n    image:\n      repository: %q\n      digest: %q\n  adapter:\n    enabled: %t\n", plan.PortForwarding.ControllerTLSSecret, runtime.Repository, runtime.Digest, plan.PortForwarding.QBitTorrentAdapterEnabled)
		if !ok || !strings.Contains(plan.Values, expected) || plan.Source.State == installStateDeployed && plan.Source.ManifestDigest == plan.Target.ManifestDigest {
			return errors.New("install plan port-forward configuration is not bound to a changed exact release")
		}
	}
	if err := plan.Source.validate(); err != nil {
		return err
	}
	if err := validateInstallCRDTransition(plan.Source, plan.TargetCRDs); err != nil {
		return err
	}
	if plan.Operation != installOperationClean && plan.Operation != installOperationTransition || plan.Operation == installOperationClean && plan.Source.State != installStateAbsent || plan.Operation == installOperationTransition && plan.Source.State != installStateDeployed {
		return errors.New("install plan operation does not match its reviewed source state")
	}
	if plan.PlanID != installPlanIdentity(plan) {
		return errors.New("install plan content does not match planID")
	}
	return nil
}

func installTransitionJournalName(release string) string {
	return chartFullname(release) + "-release-transition"
}

func loadInstallTransitionJournal(ctx context.Context, clients *Clients, namespace, release string) (InstallPlan, *corev1.ConfigMap, bool, error) {
	name := installTransitionJournalName(release)
	journal, err := clients.Kubernetes.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return InstallPlan{}, nil, false, nil
	}
	if err != nil {
		return InstallPlan{}, nil, false, fmt.Errorf("read active release transition: %w", err)
	}
	if journal.Annotations[installReleaseOwnerKey] != release || !validDigest(journal.Annotations[installTransitionPlanKey]) || journal.Immutable == nil || !*journal.Immutable || len(journal.Data) != 1 {
		return InstallPlan{}, journal, true, errors.New("active release transition journal is foreign or malformed")
	}
	encoded, ok := journal.Data[installTransitionPlanData]
	if !ok || len(encoded) > 512<<10 {
		return InstallPlan{}, journal, true, errors.New("active release transition journal lacks one bounded reviewed plan")
	}
	plan, err := decodeInstallPlan([]byte(encoded))
	if err != nil {
		return InstallPlan{}, journal, true, fmt.Errorf("decode active release transition journal: %w", err)
	}
	if plan.Namespace != namespace || plan.Release != release || plan.PlanID != journal.Annotations[installTransitionPlanKey] || plan.Operation != installOperationTransition || plan.Source.State != installStateDeployed || plan.Source.ManifestDigest == plan.Target.ManifestDigest {
		return InstallPlan{}, journal, true, errors.New("active release transition journal does not bind one changed exact release")
	}
	return plan, journal, true, nil
}

func ensureInstallTransitionJournal(ctx context.Context, clients *Clients, plan InstallPlan) (*corev1.ConfigMap, error) {
	encoded, err := EncodePlan(plan)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 512<<10 {
		return nil, errors.New("reviewed install plan is too large for the bounded transition journal")
	}
	immutable := true
	name := installTransitionJournalName(plan.Release)
	journal := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: plan.Namespace,
		Annotations: map[string]string{installReleaseOwnerKey: plan.Release, installTransitionPlanKey: plan.PlanID},
		Labels:      map[string]string{"app.kubernetes.io/managed-by": "waycloakctl", "app.kubernetes.io/component": "release-transition"},
	}, Immutable: &immutable, Data: map[string]string{installTransitionPlanData: string(encoded)}}
	created, err := clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Create(ctx, journal, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		active, existing, found, loadErr := loadInstallTransitionJournal(ctx, clients, plan.Namespace, plan.Release)
		if loadErr != nil {
			return nil, loadErr
		}
		if !found || active.PlanID != plan.PlanID || !reflect.DeepEqual(active, plan) {
			return nil, errors.New("another exact release transition is already active")
		}
		return existing, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create exact release transition journal: %w", err)
	}
	return created, nil
}

func deleteInstallTransitionJournal(ctx context.Context, clients *Clients, plan InstallPlan, required bool) error {
	active, journal, found, err := loadInstallTransitionJournal(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return err
	}
	if !found {
		if required {
			return errors.New("exact transition checkpoint lacks its immutable lifecycle journal")
		}
		return nil
	}
	if active.PlanID != plan.PlanID || !reflect.DeepEqual(active, plan) {
		return errors.New("refusing to remove a different active release transition journal")
	}
	uid := journal.UID
	if err := clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Delete(ctx, journal.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("complete exact release transition journal: %w", err)
	}
	return nil
}

func recoverInstallTransitionPlan(ctx context.Context, clients *Clients, namespace, release string, report PreflightReport, manifest ReleaseManifest, targetCRDs map[string]string) (InstallPlan, bool, error) {
	plan, _, found, err := loadInstallTransitionJournal(ctx, clients, namespace, release)
	if err != nil || !found {
		return InstallPlan{}, found, err
	}
	if report.ObservationDigest != plan.PreflightDigest || !report.Compatible {
		return InstallPlan{}, true, errors.New("active release transition no longer matches the reviewed cluster preflight observation")
	}
	if !reflect.DeepEqual(plan.Target, manifest) || !reflect.DeepEqual(plan.TargetCRDs, targetCRDs) {
		return InstallPlan{}, true, errors.New("active release transition targets a different exact release; resume or repair it before planning another target")
	}
	if _, err := observeInstallTransitionCheckpoint(ctx, clients, plan, targetCRDs); err != nil {
		return InstallPlan{}, true, fmt.Errorf("active release transition is not recoverable: %w", err)
	}
	return plan, true, nil
}

func observeInstallTransitionCheckpoint(ctx context.Context, clients *Clients, plan InstallPlan, targetCRDs map[string]string) (string, error) {
	observed, observeErr := ObserveInstalledRelease(ctx, clients, plan.Namespace, plan.Release)
	if observeErr == nil && observed.ObservationDigest == plan.Source.ObservationDigest {
		return installCheckpointSource, nil
	}
	if observeErr == nil && validateInstallTarget(plan.Source, observed, plan.Target, targetCRDs) == nil {
		return installCheckpointTarget, nil
	}
	active, _, found, err := loadInstallTransitionJournal(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return "", err
	}
	if !found || active.PlanID != plan.PlanID || !reflect.DeepEqual(active, plan) {
		if observeErr != nil {
			return "", fmt.Errorf("installed release is not observable and no matching transition journal authorizes recovery: %w", observeErr)
		}
		return "", errors.New("installed release state changed after plan review")
	}
	components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return "", fmt.Errorf("active release transition is not at an exact checkpoint: %w", err)
	}
	checkpoint, err := classifyInstallTransitionCheckpoint(components, plan)
	if err != nil {
		return "", err
	}
	return checkpoint, nil
}

func ApplyInstallPlan(ctx context.Context, clients *Clients, runner func(context.Context, string, ...string) ([]byte, error), plan InstallPlan, confirmation string) error {
	if err := plan.validate(); err != nil {
		return err
	}
	if confirmation != plan.PlanID {
		return fmt.Errorf("refusing mutation: --confirm must exactly equal %s", plan.PlanID)
	}
	if plan.PortForwarding != nil {
		current, err := observePortForwardInstallIdentity(ctx, clients, plan.Namespace, plan.PortForwarding.ControllerTLSSecret, plan.PortForwarding.QBitTorrentAdapterEnabled)
		if err != nil {
			return fmt.Errorf("refusing mutation: re-observe port-forward controller TLS identity: %w", err)
		}
		if !reflect.DeepEqual(current, *plan.PortForwarding) {
			return errors.New("refusing mutation: port-forward controller TLS identity changed after plan review")
		}
	}
	if err := ensureNoCertificateRotation(ctx, clients, plan.Namespace, plan.Release); err != nil {
		return err
	}
	if err := ensureNoInstallRepair(ctx, clients, plan.Namespace, plan.Release); err != nil {
		return err
	}
	current, err := Preflight(ctx, clients, plan.OverlayCIDR)
	if err != nil {
		return fmt.Errorf("re-run preflight before mutation: %w", err)
	}
	if !current.Compatible || current.ObservationDigest != plan.PreflightDigest {
		return errors.New("refusing mutation: cluster preflight observation changed after plan review")
	}
	if current.Cluster.Architectures[plan.NodeArchitecture] == 0 {
		return errors.New("refusing mutation: reviewed node architecture is no longer present")
	}
	if runner == nil {
		runner = defaultRunner
	}
	targetCRDs, err := ChartCRDIdentities(ctx, runner, plan.Chart)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(targetCRDs, plan.TargetCRDs) {
		return errors.New("refusing mutation: exact target chart CRD identity changed after plan review")
	}
	checkpoint, err := observeInstallTransitionCheckpoint(ctx, clients, plan, targetCRDs)
	if err != nil {
		return fmt.Errorf("refusing mutation: %w", err)
	}
	if checkpoint == installCheckpointTarget {
		if err := deleteInstallTransitionJournal(ctx, clients, plan, false); err != nil {
			return err
		}
		return nil
	}
	return applyInstallPlanAtCheckpoint(ctx, clients, runner, plan, targetCRDs, checkpoint)
}

func observePortForwardInstallIdentity(ctx context.Context, clients *Clients, namespace, name string, adapterEnabled bool) (PortForwardInstallIdentity, error) {
	identity := PortForwardInstallIdentity{ControllerTLSSecret: name, QBitTorrentAdapterEnabled: adapterEnabled}
	secret, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return PortForwardInstallIdentity{}, err
	}
	if secret.Immutable == nil || !*secret.Immutable || secret.Type != corev1.SecretTypeTLS || len(secret.Data["ca.crt"]) == 0 || len(secret.Data["tls.crt"]) == 0 || len(secret.Data["tls.key"]) == 0 {
		return PortForwardInstallIdentity{}, errors.New("port-forward controller TLS Secret must be immutable and contain ca.crt, tls.crt, and tls.key")
	}
	authorities, err := parseCABundle(secret.Data["ca.crt"])
	if err != nil {
		return PortForwardInstallIdentity{}, errors.New("port-forward controller TLS Secret has an invalid CA bundle")
	}
	pair, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil {
		return PortForwardInstallIdentity{}, errors.New("port-forward controller TLS Secret has an invalid client key pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return PortForwardInstallIdentity{}, errors.New("port-forward controller TLS Secret has an invalid client certificate")
	}
	intermediates := x509.NewCertPool()
	roots := x509.NewCertPool()
	for _, authority := range authorities {
		roots.AddCert(authority)
	}
	for _, raw := range pair.Certificate[1:] {
		certificate, parseErr := x509.ParseCertificate(raw)
		if parseErr != nil {
			return PortForwardInstallIdentity{}, errors.New("port-forward controller TLS Secret has an invalid client certificate chain")
		}
		intermediates.AddCert(certificate)
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return PortForwardInstallIdentity{}, errors.New("port-forward controller TLS Secret client certificate is not trusted for client authentication")
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != portForwardControllerSPIFFEIdentity {
		return PortForwardInstallIdentity{}, errors.New("port-forward controller TLS Secret must contain the exact replacement-controller SPIFFE identity")
	}
	identity.SecretUID = string(secret.UID)
	identity.CADigest = digestBytes(secret.Data["ca.crt"])
	identity.CertificateDigest = digestBytes(secret.Data["tls.crt"])
	if err := identity.validate(); err != nil {
		return PortForwardInstallIdentity{}, err
	}
	return identity, nil
}

func applyInstallPlanAtCheckpoint(ctx context.Context, clients *Clients, runner func(context.Context, string, ...string) ([]byte, error), plan InstallPlan, targetCRDs map[string]string, checkpoint string) error {
	if err := validateInstallCRDTransition(plan.Source, targetCRDs); err != nil {
		return err
	}
	namespace, err := clients.Kubernetes.CoreV1().Namespaces().Get(ctx, plan.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: plan.Namespace, Labels: map[string]string{
			"pod-security.kubernetes.io/enforce": "privileged", "pod-security.kubernetes.io/audit": "restricted", "pod-security.kubernetes.io/warn": "restricted",
		}}}
		if _, err = clients.Kubernetes.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create reviewed system namespace: %w", err)
		}
	} else if err != nil {
		return err
	} else if namespace.Labels["pod-security.kubernetes.io/enforce"] != "privileged" {
		return errors.New("existing system namespace is not explicitly privileged; review and label it separately before applying the plan")
	}
	caSecret, tlsSecret, err := ensureObservationSecrets(ctx, clients, plan.Namespace, plan.Release, plan.PlanID)
	if err != nil {
		return err
	}
	if !bytes.Equal(caSecret.Data["ca.crt"], tlsSecret.Data["ca.crt"]) {
		return errors.New("observation CA and serving identity do not share exact trust material")
	}
	directory, err := os.MkdirTemp("", "waycloak-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	valuesPath := filepath.Join(directory, "values.yaml")
	if err := os.WriteFile(valuesPath, []byte(plan.Values), 0o600); err != nil {
		return err
	}
	chart := plan.Chart.Repository + "@" + plan.Chart.Digest
	deployed := plan.Source.State == installStateDeployed
	changedTransition := deployed && plan.Source.ManifestDigest != plan.Target.ManifestDigest
	if changedTransition {
		if checkpoint == installCheckpointSource {
			if _, err := ensureInstallTransitionJournal(ctx, clients, plan); err != nil {
				return err
			}
		}
		if checkpoint != installCheckpointSource {
			active, _, found, err := loadInstallTransitionJournal(ctx, clients, plan.Namespace, plan.Release)
			if err != nil || !found || active.PlanID != plan.PlanID || !reflect.DeepEqual(active, plan) {
				if err != nil {
					return err
				}
				return errors.New("exact transition checkpoint lacks its matching immutable lifecycle journal")
			}
		}
	}
	if checkpoint == installCheckpointSource {
		if err := replaceGatewayClassForTransition(ctx, clients, plan.Source, plan.Target); err != nil {
			return err
		}
		if changedTransition {
			components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
			if err != nil {
				return fmt.Errorf("observe immutable gateway class withdrawal checkpoint: %w", err)
			}
			if !exactSourceComponents(components, plan.Source, true) {
				return errors.New("immutable gateway class withdrawal did not reach the exact journal-bound checkpoint")
			}
			checkpoint = installCheckpointClassWithdrawn
		}
	}
	if !deployed {
		bootstrapValuesPath := filepath.Join(directory, "controller-first-bootstrap.yaml")
		if err := os.WriteFile(bootstrapValuesPath, []byte(controllerFirstBootstrapValues), 0o600); err != nil {
			return err
		}
		output, err := runner(ctx, "helm", helmUpgradeArguments(plan, chart, valuesPath, bootstrapValuesPath)...)
		if err != nil {
			return fmt.Errorf("controller-first Helm bootstrap failed before Core activation: %w: %s", err, bounded(output, 4096))
		}
	} else if changedTransition && (checkpoint == installCheckpointClassWithdrawn || checkpoint == installCheckpointClassReplaced) {
		holdValues, err := nodeAgentTransitionHoldValues(plan.Source)
		if err != nil {
			return err
		}
		holdValuesPath := filepath.Join(directory, "node-agent-transition-hold.yaml")
		if err := os.WriteFile(holdValuesPath, []byte(holdValues), 0o600); err != nil {
			return err
		}
		output, err := runner(ctx, "helm", helmUpgradeArguments(plan, chart, valuesPath, holdValuesPath)...)
		if err != nil {
			return fmt.Errorf("helm transition staging failed with the prior node agent retained: %w: %s", err, bounded(output, 4096))
		}
		components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
		if err != nil {
			return fmt.Errorf("observe exact Helm transition staging checkpoint: %w", err)
		}
		if !exactStagedComponents(components, plan.Source, plan.Target, targetCRDs) {
			return errors.New("helm transition staging did not reach the exact journal-bound checkpoint")
		}
		checkpoint = installCheckpointStaged
	}
	output, err := runner(ctx, "helm", helmUpgradeArguments(plan, chart, valuesPath)...)
	if err != nil {
		return fmt.Errorf("helm Core activation failed; keep the deny path installed while diagnosing: %w: %s", err, bounded(output, 4096))
	}
	target, err := ObserveInstalledRelease(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return fmt.Errorf("observe exact target release after Helm: %w", err)
	}
	if err := validateInstallTarget(plan.Source, target, plan.Target, targetCRDs); err != nil {
		return err
	}
	if changedTransition {
		return deleteInstallTransitionJournal(ctx, clients, plan, true)
	}
	return nil
}

func helmUpgradeArguments(plan InstallPlan, chart string, valuesPaths ...string) []string {
	arguments := []string{"upgrade", "--install", plan.Release, chart, "--namespace", plan.Namespace, "--create-namespace", "--server-side=true", "--force-conflicts"}
	for _, path := range valuesPaths {
		arguments = append(arguments, "--values", path)
	}
	return append(arguments, "--wait", "--timeout", "10m")
}

func nodeAgentTransitionHoldValues(source InstalledReleaseObservation) (string, error) {
	reference := source.Images["waycloak-node-agent"]
	separator := strings.LastIndex(reference, "@")
	if separator < 1 || !validDigest(reference[separator+1:]) {
		return "", errors.New("reviewed source lacks an exact node-agent image for transition staging")
	}
	return fmt.Sprintf(`nodeAgent:
  image:
    repository: %q
    digest: %q
  releaseIdentity:
    version: %q
    manifestDigest: %q
`, reference[:separator], reference[separator+1:], source.Version, source.ManifestDigest), nil
}

func replaceGatewayClassForTransition(ctx context.Context, clients *Clients, source InstalledReleaseObservation, target ReleaseManifest) error {
	if source.State != installStateDeployed || source.ManifestDigest == target.ManifestDigest {
		return nil
	}
	uid := types.UID(source.GatewayClassUID)
	if err := clients.Dynamic.Resource(gatewayClassGVR).Delete(ctx, "gluetun.waycloak.io", metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		return fmt.Errorf("replace exact immutable gateway class for release transition: %w", err)
	}
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := clients.Dynamic.Resource(gatewayClassGVR).Get(ctx, "gluetun.waycloak.io", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}); err != nil {
		return fmt.Errorf("wait for exact immutable gateway class replacement boundary: %w", err)
	}
	return nil
}

func ensureObservationSecrets(ctx context.Context, clients *Clients, namespace, release, planID string) (*corev1.Secret, *corev1.Secret, error) {
	secrets := clients.Kubernetes.CoreV1().Secrets(namespace)
	caName := release + "-observation-ca"
	tlsName := release + "-observation-tls"
	caSecret, caErr := secrets.Get(ctx, caName, metav1.GetOptions{})
	tlsSecret, tlsErr := secrets.Get(ctx, tlsName, metav1.GetOptions{})
	if caErr != nil && !apierrors.IsNotFound(caErr) {
		return nil, nil, caErr
	}
	if tlsErr != nil && !apierrors.IsNotFound(tlsErr) {
		return nil, nil, tlsErr
	}
	caFound := caErr == nil
	tlsFound := tlsErr == nil
	if caFound {
		if err := validateReleaseOwnedInstallSecret(caSecret, release, corev1.SecretTypeOpaque); err != nil {
			return nil, nil, err
		}
	}
	if tlsFound {
		if err := validateReleaseOwnedInstallSecret(tlsSecret, release, corev1.SecretTypeTLS); err != nil {
			return nil, nil, err
		}
	}
	if caFound && !tlsFound {
		return nil, nil, errors.New("installation CA exists without its serving identity; use the explicit certificate-rotation recovery procedure")
	}
	if !tlsFound {
		caPEM, certPEM, keyPEM, err := observationIdentity(release, namespace)
		if err != nil {
			return nil, nil, err
		}
		tlsSecret, err = createReleaseOwnedSecret(ctx, clients, namespace, tlsName, release, planID, corev1.SecretTypeTLS, map[string][]byte{"ca.crt": caPEM, "tls.crt": certPEM, "tls.key": keyPEM})
		if err != nil {
			return nil, nil, err
		}
		tlsFound = true
	}
	if !caFound {
		var err error
		caSecret, err = createReleaseOwnedSecret(ctx, clients, namespace, caName, release, planID, corev1.SecretTypeOpaque, map[string][]byte{"ca.crt": tlsSecret.Data["ca.crt"]})
		if err != nil {
			return nil, nil, err
		}
	}
	if !tlsFound || !bytes.Equal(caSecret.Data["ca.crt"], tlsSecret.Data["ca.crt"]) {
		return nil, nil, errors.New("observation CA and serving identity do not share exact trust material")
	}
	return caSecret, tlsSecret, nil
}

func createReleaseOwnedSecret(ctx context.Context, clients *Clients, namespace, name, release, planID string, secretType corev1.SecretType, data map[string][]byte) (*corev1.Secret, error) {
	created, err := clients.Kubernetes.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: map[string]string{installReleaseOwnerKey: release, installInitialPlanKey: planID}}, Type: secretType, Data: data}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		created, err = clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, err
	}
	if err := validateReleaseOwnedInstallSecret(created, release, secretType); err != nil {
		return nil, err
	}
	return created, nil
}

func validateInstallSecret(secret *corev1.Secret, secretType corev1.SecretType) error {
	if secret.Type != secretType || len(secret.Data["ca.crt"]) == 0 {
		return errors.New("existing installation Secret has an invalid type or CA")
	}
	authorities, err := parseCABundle(secret.Data["ca.crt"])
	if err != nil {
		return err
	}
	if secretType == corev1.SecretTypeOpaque {
		if len(secret.Data) != 1 {
			return errors.New("installation CA Secret contains unexpected keys")
		}
		return nil
	}
	return validateServingIdentity(secret, authorities)
}

func observationIdentity(release, namespace string) ([]byte, []byte, []byte, error) {
	now := time.Now().UTC()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, nil, err
	}
	caTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Waycloak installation CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, nil, err
	}
	dnsName := chartFullname(release) + "-controller." + namespace + ".svc"
	serverTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: dnsName}, DNSNames: []string{dnsName, chartFullname(release) + "-controller." + namespace + ".svc.cluster.local"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, serverPublic, caPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func defaultRunner(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, arguments...).CombinedOutput()
}

func bounded(value []byte, limit int) string {
	if len(value) > limit {
		value = value[:limit]
	}
	return string(value)
}
