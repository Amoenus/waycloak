// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCertificateRotationPlanIsCanonicalAndConfirmationBound(t *testing.T) {
	ctx := context.Background()
	clients, plan, _ := certificateRotationFixture(t)
	encoded, err := EncodeCertificateRotationPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("PRIVATE KEY")) || bytes.Contains(encoded, []byte("tls.key")) {
		t.Fatal("certificate rotation plan contains private material")
	}
	path := filepath.Join(t.TempDir(), "rotation.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCertificateRotationPlan(path)
	if err != nil || !reflect.DeepEqual(loaded, plan) {
		t.Fatalf("canonical rotation plan did not round-trip: %#v %v", loaded, err)
	}

	var tampered map[string]any
	if err := json.Unmarshal(encoded, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["release"] = "different"
	tamperedData, _ := json.Marshal(tampered)
	if _, err := decodeCertificateRotationPlan(tamperedData); err == nil || !strings.Contains(err.Error(), "planID") {
		t.Fatalf("tampered rotation plan was accepted: %v", err)
	}
	if err := ApplyCertificateRotationPlan(ctx, clients, plan, "wrong"); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("wrong confirmation was accepted: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, certificateRotationStagedSecretName(plan.Release), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("refused rotation staged a private identity: %v", err)
	}
}

func TestCertificateRotationClassifiesEveryExactInterruption(t *testing.T) {
	ctx := context.Background()
	clients, plan, report := certificateRotationFixture(t)
	staged, err := ensureStagedCertificate(ctx, clients, plan)
	if err != nil {
		t.Fatal(err)
	}
	journal, object, err := ensureCertificateRotationJournal(ctx, clients, plan, staged)
	if err != nil {
		t.Fatal(err)
	}
	if object.Immutable == nil || !*object.Immutable || strings.Contains(object.Data[certificateRotationData], "PRIVATE KEY") || strings.Contains(object.Data[certificateRotationData], "tls.key") {
		t.Fatal("rotation journal is mutable or contains private material")
	}
	assertRotationPhase(t, clients, journal, staged, rotationPhaseSource)

	sourceSecret, err := getRotationSecret(ctx, clients, plan.Namespace, plan.Release+"-observation-ca")
	if err != nil {
		t.Fatal(err)
	}
	sourceCA := sourceSecret.Data["ca.crt"]
	overlap := appendPEMBundle(sourceCA, staged.Data["ca.crt"])
	if err := updateRotationCASecret(ctx, clients, plan, overlap); err != nil {
		t.Fatal(err)
	}
	assertRotationPhase(t, clients, journal, staged, rotationPhaseOverlapCAOnly)
	if err := updateRotationTLSSecret(ctx, clients, plan, overlap, nil); err != nil {
		t.Fatal(err)
	}
	assertRotationPhase(t, clients, journal, staged, rotationPhaseOverlapOldAgent)
	setTestRotationState(t, clients, plan, plan.PlanID+"-overlap", true)
	assertRotationPhase(t, clients, journal, staged, rotationPhaseOverlapNewAgent)
	if err := updateRotationTLSSecret(ctx, clients, plan, overlap, staged); err != nil {
		t.Fatal(err)
	}
	assertRotationPhase(t, clients, journal, staged, rotationPhaseServingSwitched)
	if err := updateRotationTLSSecret(ctx, clients, plan, staged.Data["ca.crt"], staged); err != nil {
		t.Fatal(err)
	}
	assertRotationPhase(t, clients, journal, staged, rotationPhasePrunedTLSOnly)
	if err := updateRotationCASecret(ctx, clients, plan, staged.Data["ca.crt"]); err != nil {
		t.Fatal(err)
	}
	assertRotationPhase(t, clients, journal, staged, rotationPhasePrunedOldAgent)
	setTestRotationState(t, clients, plan, plan.PlanID+"-new-only", true)
	assertRotationPhase(t, clients, journal, staged, rotationPhasePrunedNewAgentHeld)
	setTestRotationState(t, clients, plan, plan.PlanID, false)
	assertRotationPhase(t, clients, journal, staged, rotationPhaseTarget)

	target, err := ObserveInstalledRelease(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	if target.ObservationRotationID != plan.PlanID || target.ObservationCADigest != journal.TargetCADigest || target.ObservationServingDigest != journal.TargetServingDigest || target.ObservationCAUID != plan.Source.ObservationCAUID || target.ObservationTLSUID != plan.Source.ObservationTLSUID {
		t.Fatalf("completed rotation lost stable or target identity: %#v", target)
	}
	installPlan, err := BuildInstallPlan(releaseManifest(), plan.Namespace, plan.Release, "", report, target, target.CRDIdentities, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(installPlan.Values, "observationRotationID: \""+plan.PlanID+"\"") {
		t.Fatalf("later exact release plan did not preserve completed trust identity: %s", installPlan.Values)
	}

	staged.Data["tls.crt"] = []byte("tampered")
	if err := validateStagedCertificate(journal, staged); err == nil {
		t.Fatal("tampered staged identity was accepted")
	}
}

func TestCertificateRotationRecoversOnlyExactActivePlan(t *testing.T) {
	ctx := context.Background()
	clients, plan, report := certificateRotationFixture(t)
	staged, err := ensureStagedCertificate(ctx, clients, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureCertificateRotationJournal(ctx, clients, plan, staged); err != nil {
		t.Fatal(err)
	}

	recovered, found, err := recoverCertificateRotationPlan(ctx, clients, plan.Namespace, plan.Release, report)
	if err != nil || !found || !reflect.DeepEqual(recovered, plan) {
		t.Fatalf("active exact rotation was not recoverable: found=%t plan=%#v err=%v", found, recovered, err)
	}

	drifted := report
	drifted.ObservationDigest = digestBytes([]byte("different-preflight"))
	if _, found, err := recoverCertificateRotationPlan(ctx, clients, plan.Namespace, plan.Release, drifted); err == nil || !found || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("drifted preflight recovered an active rotation: found=%t err=%v", found, err)
	}
}

func TestCertificateRotationRecoversStagedIdentityBeforeJournal(t *testing.T) {
	ctx := context.Background()
	clients, plan, report := certificateRotationFixture(t)
	staged, err := ensureStagedCertificate(ctx, clients, plan)
	if err != nil {
		t.Fatal(err)
	}
	originalUID, originalKey := staged.UID, append([]byte(nil), staged.Data["tls.key"]...)

	rebuilt, err := BuildCertificateRotationPlan(report, plan.Source, plan.Namespace, plan.Release, plan.OverlayCIDR)
	if err != nil || !reflect.DeepEqual(rebuilt, plan) {
		t.Fatalf("reconstructed plan changed after staging: %#v %v", rebuilt, err)
	}
	if err := validateNoOrMatchingStagedCertificate(ctx, clients, rebuilt); err != nil {
		t.Fatalf("matching immutable staged identity was not adoptable: %v", err)
	}
	adopted, err := ensureStagedCertificate(ctx, clients, rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.UID != originalUID || !bytes.Equal(adopted.Data["tls.key"], originalKey) {
		t.Fatal("staged identity was regenerated instead of adopted")
	}
	journal, _, err := ensureCertificateRotationJournal(ctx, clients, rebuilt, adopted)
	if err != nil || journal.StagedSecretUID != string(originalUID) {
		t.Fatalf("adopted staged identity was not durably journaled: %#v %v", journal, err)
	}
}

func TestCertificateRotationRejectsLostStagedIdentityBeforeTarget(t *testing.T) {
	ctx := context.Background()
	clients, plan, report := certificateRotationFixture(t)
	staged, err := ensureStagedCertificate(ctx, clients, plan)
	if err != nil {
		t.Fatal(err)
	}
	journal, object, err := ensureCertificateRotationJournal(ctx, clients, plan, staged)
	if err != nil {
		t.Fatal(err)
	}
	if err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Delete(ctx, staged.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, found, err := recoverCertificateRotationPlan(ctx, clients, plan.Namespace, plan.Release, report); err == nil || !found || !strings.Contains(err.Error(), "lost staged private material") {
		t.Fatalf("lost staged key material was treated as recoverable: found=%t err=%v", found, err)
	}
	if err := ApplyCertificateRotationPlan(ctx, clients, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "lost staged private material") {
		t.Fatalf("apply regenerated lost staged key material: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Get(ctx, object.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("failed recovery removed its evidence journal: %v", err)
	}
	if completed, err := completedRotationWithoutStaged(ctx, clients, journal); err != nil || completed {
		t.Fatalf("source checkpoint was misclassified as completed: completed=%t err=%v", completed, err)
	}
}

func TestCertificateRotationRefusesPreflightDriftBeforeStaging(t *testing.T) {
	ctx := context.Background()
	clients, plan, _ := certificateRotationFixture(t)
	nodes, err := clients.Kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		t.Fatalf("fixture lacks a node: %#v %v", nodes.Items, err)
	}
	node := nodes.Items[0].DeepCopy()
	node.Status.NodeInfo.KernelVersion += "-drift"
	if _, err := clients.Kubernetes.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := ApplyCertificateRotationPlan(ctx, clients, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "preflight changed") {
		t.Fatalf("preflight drift was accepted: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, certificateRotationStagedSecretName(plan.Release), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("preflight refusal staged private material: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Get(ctx, certificateRotationJournalName(plan.Release), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("preflight refusal created a journal: %v", err)
	}
}

func TestActiveCertificateRotationBlocksReleaseChanges(t *testing.T) {
	ctx := context.Background()
	clients, plan, _ := certificateRotationFixture(t)
	staged, err := ensureStagedCertificate(ctx, clients, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureCertificateRotationJournal(ctx, clients, plan, staged); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoCertificateRotation(ctx, clients, plan.Namespace, plan.Release); err == nil || !strings.Contains(err.Error(), "rotation is active") {
		t.Fatalf("active certificate rotation did not block release mutation: %v", err)
	}
}

func TestReleaseCheckpointRejectsCertificateCapabilityHold(t *testing.T) {
	ctx := context.Background()
	clients, plan, _ := certificateRotationFixture(t)
	components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	if !exactSourceComponents(components, plan.Source, false) {
		t.Fatal("exact source fixture was not recognized")
	}
	components.ObservationCapabilityHeld = true
	if exactSourceComponents(components, plan.Source, false) {
		t.Fatal("ordinary release transition accepted a held node capability checkpoint")
	}
}

func TestCertificateRotationRecoversFinalCleanupAfterStagedDeletion(t *testing.T) {
	ctx := context.Background()
	clients, plan, _ := certificateRotationFixture(t)
	staged, err := ensureStagedCertificate(ctx, clients, plan)
	if err != nil {
		t.Fatal(err)
	}
	journal, object, err := ensureCertificateRotationJournal(ctx, clients, plan, staged)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateRotationTLSSecret(ctx, clients, plan, staged.Data["ca.crt"], staged); err != nil {
		t.Fatal(err)
	}
	if err := updateRotationCASecret(ctx, clients, plan, staged.Data["ca.crt"]); err != nil {
		t.Fatal(err)
	}
	setTestRotationState(t, clients, plan, plan.PlanID, false)
	assertRotationPhase(t, clients, journal, staged, rotationPhaseTarget)
	if err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Delete(ctx, staged.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	completed, err := completedRotationWithoutStaged(ctx, clients, journal)
	if err != nil || !completed {
		t.Fatalf("exact final cleanup was not recoverable: completed=%t err=%v", completed, err)
	}
	if err := deleteCertificateRotationJournal(ctx, clients, journal); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Get(ctx, object.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("completed cleanup retained its journal: %v", err)
	}
}

func TestCertificateBundlesRejectDuplicatesAndUntrustedServingPairs(t *testing.T) {
	caOne, certOne, keyOne, err := observationIdentity("waycloak", "waycloak-system")
	if err != nil {
		t.Fatal(err)
	}
	caTwo, _, _, err := observationIdentity("waycloak", "waycloak-system")
	if err != nil {
		t.Fatal(err)
	}
	authorities, err := parseCABundle(appendPEMBundle(caOne, caTwo))
	if err != nil || len(authorities) != 2 {
		t.Fatalf("valid bounded overlap was rejected: %#v %v", authorities, err)
	}
	if _, err := parseCABundle(appendPEMBundle(caOne, caOne)); err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("duplicate CA was accepted: %v", err)
	}
	if _, err := parseCABundle(append(caOne, []byte("trailing")...)); err == nil {
		t.Fatal("trailing CA bundle data was accepted")
	}
	secret := &corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"ca.crt": caTwo, "tls.crt": certOne, "tls.key": keyOne}}
	if err := validateInstallSecret(secret, corev1.SecretTypeTLS); err == nil || !strings.Contains(err.Error(), "another CA") {
		t.Fatalf("untrusted serving pair was accepted: %v", err)
	}
}

func certificateRotationFixture(t *testing.T) (*Clients, CertificateRotationPlan, PreflightReport) {
	t.Helper()
	ctx := context.Background()
	clients := supportedClients(t)
	if _, err := clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "waycloak-system", Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("7", 64)); err != nil {
		t.Fatal(err)
	}
	crds, _, _ := testInstallCRDBundle(t)
	seedInstalledRelease(t, clients, releaseManifest(), "waycloak-system", "waycloak", 1, crds)
	source, err := ObserveInstalledRelease(ctx, clients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Preflight(ctx, clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildCertificateRotationPlan(report, source, "waycloak-system", "waycloak", "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	return clients, plan, report
}

func assertRotationPhase(t *testing.T, clients *Clients, journal certificateRotationJournal, staged *corev1.Secret, wanted string) {
	t.Helper()
	phase, err := observeCertificateRotationPhase(context.Background(), clients, journal, staged)
	if err != nil || phase != wanted {
		t.Fatalf("rotation phase=%q, want %q: %v", phase, wanted, err)
	}
}

func setTestRotationState(t *testing.T, clients *Clients, plan CertificateRotationPlan, rotationID string, capabilityHold bool) {
	t.Helper()
	name := chartFullname(plan.Release) + "-node-agent"
	daemonSet, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if daemonSet.Spec.Template.Annotations == nil {
		daemonSet.Spec.Template.Annotations = map[string]string{}
	}
	daemonSet.Spec.Template.Annotations[observationRotationKey] = rotationID
	arguments := daemonSet.Spec.Template.Spec.Containers[0].Args[:0]
	for _, argument := range daemonSet.Spec.Template.Spec.Containers[0].Args {
		if !strings.HasPrefix(argument, "--observation-capability-hold=") {
			arguments = append(arguments, argument)
		}
	}
	if capabilityHold {
		arguments = append(arguments, "--observation-capability-hold=true")
	}
	daemonSet.Spec.Template.Spec.Containers[0].Args = arguments
	if _, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Update(context.Background(), daemonSet, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}
